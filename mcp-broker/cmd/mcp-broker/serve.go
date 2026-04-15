package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/telegram"
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
	serveCmd.Flags().BoolP("verbose", "v", false, "enable debug logging (shorthand for --log-level=debug)")
	serveCmd.Flags().Bool("no-open", false, "do not open dashboard in browser")
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

	logLevel, _ := cmd.Flags().GetString("log-level")
	verbose, _ := cmd.Flags().GetBool("verbose")
	if logLevel != "" && verbose {
		return fmt.Errorf("cannot use --verbose and --log-level together")
	}
	if verbose {
		cfg.Log.Level = "debug"
	} else if logLevel != "" {
		cfg.Log.Level = logLevel
	}

	level := parseLogLevel(cfg.Log.Level)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	logger.Info("config loaded", "path", configPath())

	// Load or generate auth token.
	tokenPath := auth.TokenPath()
	token, err := auth.EnsureToken(tokenPath)
	if err != nil {
		return fmt.Errorf("loading auth token: %w", err)
	}
	logger.Info("auth token loaded", "path", tokenPath)

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
	dash := dashboard.New(mgr, engine, auditor, logger.With("component", "dashboard"))

	// Create multi-approver
	timeout := time.Duration(cfg.ApprovalTimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	approvers := []broker.Approver{dash}
	if cfg.Telegram.Enabled {
		tgToken := os.ExpandEnv(cfg.Telegram.Token)
		tgChatID := os.ExpandEnv(cfg.Telegram.ChatID)
		tg := telegram.New(tgToken, tgChatID, logger.With("component", "telegram"))
		tg.WithTools(mgr)
		approvers = append(approvers, tg)
		logger.Info("telegram approver enabled", "chat_id", tgChatID)
	}
	multi := broker.NewMultiApprover(timeout, approvers...)

	// Create broker (grants engine wired in Task 11)
	b := broker.New(mgr, engine, auditor, multi, logger.With("component", "broker"), nil)

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

	// Mount dashboard at /dashboard
	dashHandler := dash.Handler()
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashHandler))

	// Redirect root to dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: auth.Middleware(token, mux), ReadHeaderTimeout: 10 * time.Second}

	// Handle shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		fmt.Fprintf(os.Stderr, "Dashboard: http://localhost:%d/dashboard/?token=%s\n", cfg.Port, token)
		errCh <- srv.ListenAndServe()
	}()

	// Open browser if enabled
	noOpen, _ := cmd.Flags().GetBool("no-open")
	if cfg.OpenBrowser && !noOpen {
		dashURL := fmt.Sprintf("http://localhost:%d/dashboard/?token=%s", cfg.Port, token)
		logger.Debug("opening browser")
		if err := openBrowser(dashURL); err != nil {
			logger.Warn("failed to open browser", "error", err)
		}
	}

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
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec // url is constructed internally, not user input
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec // url is constructed internally, not user input
	}
	return cmd.Start()
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
