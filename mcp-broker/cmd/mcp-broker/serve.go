package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP broker",
	RunE:  runServe,
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if v, _ := cmd.Flags().GetString("log-level"); v != "" {
		cfg.Log.Level = v
	}

	level := parseLogLevel(cfg.Log.Level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	logger.Info("config loaded", "path", configPath())

	// Create audit logger
	auditor, err := audit.NewLogger(cfg.Audit.Path)
	if err != nil {
		return fmt.Errorf("creating audit logger: %w", err)
	}
	defer func() { _ = auditor.Close(context.Background()) }()

	// Connect to backend servers
	ctx := context.Background()
	mgr, err := server.NewManager(ctx, cfg.Servers, logger.With("component", "server"))
	if err != nil {
		return fmt.Errorf("creating server manager: %w", err)
	}
	defer func() { _ = mgr.Close() }()

	tools := mgr.Tools()
	logger.Info("tools discovered", "count", len(tools))

	// Create rules engine
	engine := rules.New(cfg.Rules)

	// Create dashboard
	dash := dashboard.New(mgr, auditor, logger.With("component", "dashboard"))

	// Create broker
	b := broker.New(mgr, engine, auditor, dash, logger.With("component", "broker"))

	// Create MCP server
	mcpSrv := mcpserver.NewMCPServer("mcp-broker", "0.1.0")
	for _, tool := range tools {
		mcpTool := toolToMCPTool(tool)
		mcpSrv.AddTool(mcpTool, makeMCPHandler(b))
	}

	// Create combined HTTP server
	mux := http.NewServeMux()

	// Mount MCP at /mcp
	streamHandler := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux.Handle("/mcp", streamHandler)

	// Mount dashboard at / (everything else)
	dashHandler := dash.Handler()
	mux.Handle("/", dashHandler)

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	// Handle shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-stop:
		logger.Info("shutting down, send again to force exit")
		go func() {
			<-stop
			logger.Warn("forced shutdown")
			os.Exit(1)
		}()
		return srv.Shutdown(context.Background())
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

func toolToMCPTool(t server.Tool) gomcp.Tool {
	props := make(map[string]any)
	var required []string

	if t.InputSchema != nil {
		if p, ok := t.InputSchema["properties"].(map[string]any); ok {
			props = p
		}
		if r, ok := t.InputSchema["required"].([]string); ok {
			required = r
		}
		// Handle []any from JSON unmarshaling
		if r, ok := t.InputSchema["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
	}

	return gomcp.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: gomcp.ToolInputSchema{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
	}
}

func makeMCPHandler(b *broker.Broker) func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		if args == nil {
			args = make(map[string]any)
		}

		result, err := b.Handle(ctx, req.Params.Name, args)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		// Wrap slice results for MCP compliance
		if _, ok := result.([]any); ok {
			result = map[string]any{"items": result}
		}

		// Marshal to JSON text for the tool result
		data, err := json.Marshal(result)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(string(data)), nil
	}
}
