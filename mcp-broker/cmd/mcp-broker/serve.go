package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/hooks"
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

const shutdownTimeout = 10 * time.Second

var forceExit = os.Exit

type stoppableServer interface {
	Shutdown(context.Context) error
	Close() error
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
	cfgPath := configPath()
	cfg, err := config.Load(cfgPath)
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
	logger.Info("config loaded", "path", cfgPath)

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()

	var (
		auditor          *audit.Logger
		grantStore       *grants.Store
		hookDispatcher   *hooks.Dispatcher
		mgr              *server.Manager
		unsubscribeAudit func()
		closeOnce        sync.Once
		closeErr         error
		coordinated      bool
	)
	closeResources := func(ctx context.Context) error {
		closeOnce.Do(func() {
			if unsubscribeAudit != nil {
				unsubscribeAudit()
			}
			var errs []error
			if hookDispatcher != nil {
				if err := hookDispatcher.Close(ctx); err != nil {
					errs = append(errs, fmt.Errorf("closing hook dispatcher: %w", err))
				}
			}
			if mgr != nil {
				if err := mgr.Close(); err != nil {
					errs = append(errs, fmt.Errorf("closing server manager: %w", err))
				}
			}
			if grantStore != nil {
				if err := grantStore.Close(); err != nil {
					errs = append(errs, fmt.Errorf("closing grants store: %w", err))
				}
			}
			if auditor != nil {
				if err := auditor.Close(ctx); err != nil {
					errs = append(errs, fmt.Errorf("closing audit logger: %w", err))
				}
			}
			closeErr = errors.Join(errs...)
		})
		return closeErr
	}
	defer func() {
		if !coordinated {
			cancelLifetime()
			_ = closeResources(context.Background())
		}
	}()

	rulesResult, err := config.LoadRulesForConfig(cfgPath, cfg)
	if err != nil {
		return fmt.Errorf("loading rules: %w", err)
	}
	logger.Info("rules loaded", "path", rulesResult.Path, "count", len(rulesResult.Rules))
	if rulesResult.MigratedLegacy {
		logger.Warn("legacy config rules migrated", "config_path", cfgPath, "rules_path", rulesResult.Path)
	}
	if rulesResult.IgnoredLegacy {
		logger.Warn("legacy config rules ignored because rules file exists", "config_path", cfgPath, "rules_path", rulesResult.Path)
	}

	// Load or migrate both strictly separated role credentials before binding.
	tokenPaths := auth.DefaultTokenPaths()
	tokens, err := auth.EnsureTokenSetContext(commandContext(cmd), tokenPaths)
	if err != nil {
		return fmt.Errorf("loading role credentials: %w", err)
	}
	authStore, err := auth.NewStore(tokens)
	if err != nil {
		return fmt.Errorf("creating auth store: %w", err)
	}
	logger.Info("role credentials loaded", "agent_path", tokenPaths.Agent, "admin_path", tokenPaths.Admin)

	// Create audit logger
	auditor, err = audit.NewLogger(cfg.Audit.Path)
	if err != nil {
		return fmt.Errorf("creating audit logger: %w", err)
	}

	// Create grant store. Grant lookup errors are authorization state failures, so startup fails if unavailable.
	grantStore, err = grants.Open(cfg.Grants.Path)
	if err != nil {
		return fmt.Errorf("creating grants store: %w", err)
	}

	// Connect to backend servers
	mgr, err = server.NewManager(lifetimeCtx, cfg.Servers, cfg.ToolPatches, logger.With("component", "server"))
	if err != nil {
		return fmt.Errorf("creating server manager: %w", err)
	}

	tools := mgr.Tools()
	logger.Info("tools discovered", "count", len(tools))

	// Create reloadable rules store
	ruleStore, err := rules.NewStore(rulesResult.Rules)
	if err != nil {
		return fmt.Errorf("compiling rules: %w", err)
	}

	// Create dashboard
	dash := dashboard.NewWithGrants(mgr, ruleStore, auditor, grantStore, logger.With("component", "dashboard"))

	// Wire audit subscriber so live records are broadcast over SSE.
	unsubscribeAudit = auditor.Subscribe(dash.OnAuditRecord)

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

	// Create startup-only hook dispatcher and broker.
	hookDispatcher = hooks.New(lifetimeCtx, cfg.Hooks, logger.With("component", "hooks"))
	b := broker.NewWithOptions(broker.Options{
		Servers: mgr, Rules: ruleStore, Auditor: auditor, Approver: multi,
		Grants: grantStore, Observer: hookDispatcher, Logger: logger.With("component", "broker"),
	})

	// Create MCP server
	mcpSrv := mcpserver.NewMCPServer("mcp-broker", "0.1.0")
	for _, tool := range tools {
		mcpTool := toolToMCPTool(tool)
		mcpSrv.AddTool(mcpTool, makeMCPHandler(b))
	}

	// Create combined HTTP server
	mux := http.NewServeMux()

	// Mount unauthenticated liveness check at /healthz
	mux.HandleFunc("GET /healthz", handleHealthz)

	// Mount MCP at /mcp
	streamHandler := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux.Handle("/mcp", limitRequestBody(cfg.MaxRequestBodyBytes, streamHandler))

	// Mount dashboard at /dashboard
	dashHandler := dash.Handler()
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashHandler))

	// Redirect root to dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if err := server.ValidateLoopbackAddr(addr); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           auth.Middleware(authStore, mux),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return lifetimeCtx
		},
	}

	var shutdownOnce sync.Once
	var shutdownErr error
	shutdown := func() error {
		shutdownOnce.Do(func() {
			shutdownErr = shutdownApplication(cancelLifetime, srv, closeResources, logger, shutdownTimeout)
		})
		return shutdownErr
	}
	coordinated = true
	defer func() { _ = shutdown() }()

	// Handle shutdown and rules reload.
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	noOpen, _ := cmd.Flags().GetBool("no-open")
	announceDashboard(cfg.Port, authStore.Snapshot().Admin, os.Stdout, cfg.OpenBrowser && !noOpen, isInteractiveOutput, openBrowser, logger)

	return serveEventLoop(stop, reload, errCh, logger, func() error {
		return reloadBrokerState(func() error {
			return reloadRulesFromFile(rulesResult.Path, ruleStore, logger)
		}, authStore, tokenPaths, logger)
	}, shutdown)
}

func serveEventLoop(stop <-chan os.Signal, reload <-chan os.Signal, errCh <-chan error, logger *slog.Logger, reloadRules func() error, shutdown func() error) error {
	for {
		select {
		case <-reload:
			if err := reloadRules(); err != nil && logger != nil {
				logger.Warn("rules reload failed", "error", err)
			}
		case <-stop:
			if logger != nil {
				logger.Info("shutting down, send again to force exit")
			}
			go func() {
				<-stop
				if logger != nil {
					logger.Warn("forced shutdown")
				}
				forceExit(1)
			}()
			return shutdown()
		case err := <-errCh:
			if !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("server error: %w", err)
			}
			return nil
		}
	}
}

func reloadBrokerState(reloadRules func() error, store *auth.Store, paths auth.TokenPaths, logger *slog.Logger) error {
	var errs []error
	if err := reloadRules(); err != nil {
		errs = append(errs, err)
	}
	result := store.Reload(paths)
	if result.AgentErr != nil {
		errs = append(errs, fmt.Errorf("reloading agent token: %w", result.AgentErr))
	}
	if result.AdminErr != nil {
		errs = append(errs, fmt.Errorf("reloading admin token: %w", result.AdminErr))
	}
	if logger != nil && result.AgentErr == nil && result.AdminErr == nil {
		logger.Info("role credentials reloaded", "agent_path", paths.Agent, "admin_path", paths.Admin)
	}
	return errors.Join(errs...)
}

func announceDashboard(port int, adminToken string, out io.Writer, open bool, interactive func(io.Writer) bool, opener func(string) error, logger *slog.Logger) {
	if !interactive(out) {
		if logger != nil {
			logger.Debug("not announcing dashboard because output is not interactive", "port", port)
		}
		return
	}
	dashboardURL := fmt.Sprintf("http://localhost:%d/dashboard/?token=%s", port, adminToken)
	_, _ = fmt.Fprintf(out, "Dashboard: %s\n", dashboardURL)
	if !open {
		return
	}
	if err := opener(dashboardURL); err != nil && logger != nil {
		logger.Warn("opening dashboard in a browser failed", "error", err, "port", port)
	}
}

func isInteractiveOutput(out io.Writer) bool {
	file, ok := out.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func reloadRulesFromFile(path string, store *rules.Store, logger *slog.Logger) error {
	ruleConfigs, err := config.LoadRulesFile(path)
	if err != nil {
		return fmt.Errorf("loading rules file: %w", err)
	}
	if err := store.Reload(ruleConfigs); err != nil {
		return fmt.Errorf("compiling rules: %w", err)
	}
	if logger != nil {
		logger.Info("rules reloaded", "path", path, "count", len(ruleConfigs))
	}
	return nil
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func limitRequestBody(maxBytes int64, next http.Handler) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBytes {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

func shutdownApplication(cancelLifetime context.CancelFunc, srv stoppableServer, closeResources func(context.Context) error, logger *slog.Logger, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cancelLifetime()

	var (
		forceCloseOnce sync.Once
		forceCloseErr  error
	)
	forceClose := func() error {
		forceCloseOnce.Do(func() {
			forceCloseErr = srv.Close()
		})
		return forceCloseErr
	}

	done := make(chan error, 1)
	go func() {
		var errs []error
		if err := srv.Shutdown(ctx); err != nil {
			if closeErr := forceClose(); closeErr != nil {
				errs = append(errs, fmt.Errorf("forcing HTTP shutdown: %w", closeErr))
			}
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				errs = append(errs, fmt.Errorf("shutting down HTTP server: %w", err))
			}
		}
		if closeResources != nil {
			if err := closeResources(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		done <- errors.Join(errs...)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if logger != nil {
			logger.Warn("application shutdown timed out, forcing exit", "timeout", timeout)
		}
		go func() {
			if err := forceClose(); err != nil && logger != nil {
				logger.Warn("forced HTTP close failed", "error", err)
			}
		}()
		return ctx.Err()
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

	out := gomcp.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: gomcp.ToolInputSchema{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
		Meta: t.Meta,
	}
	if t.OutputSchema != nil {
		out.OutputSchema = *t.OutputSchema
	}
	if t.Annotations != nil {
		out.Annotations = *t.Annotations
	}
	return out
}

func parseApprovalMode(raw string) (broker.ApprovalMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "wait":
		return broker.ApprovalModeWait, nil
	case string(broker.ApprovalModeReject):
		return broker.ApprovalModeReject, nil
	default:
		return "", fmt.Errorf("unsupported Mcp-Broker-Approval-Mode %q (expected \"wait\" or \"reject\")", raw)
	}
}

func parseGrantHeader(h http.Header) (string, string) {
	values := h.Values("Mcp-Broker-Grant")
	if len(values) == 0 {
		return "", ""
	}
	if len(values) > 1 {
		return "", "multiple Mcp-Broker-Grant headers are not allowed"
	}
	value := values[0]
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", "Mcp-Broker-Grant must not be empty"
	}
	if strings.Contains(value, ",") {
		return "", "comma-combined Mcp-Broker-Grant values are not allowed"
	}
	if trimmed != value || strings.ContainsAny(value, " \t\r\n") {
		return "", "Mcp-Broker-Grant must be a single token"
	}
	return value, ""
}

func makeMCPHandler(b *broker.Broker) func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	return func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		if args == nil {
			args = make(map[string]any)
		}

		approvalMode, err := parseApprovalMode(req.Header.Get("Mcp-Broker-Approval-Mode"))
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		grantToken, grantHeaderError := parseGrantHeader(req.Header)

		result, err := b.HandleToolResultWithOptions(ctx, req.Params.Name, args, broker.HandleOptions{ApprovalMode: approvalMode, GrantToken: grantToken, GrantHeaderError: grantHeaderError})
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if result.StructuredContent != nil {
			content, ok := result.Content.([]gomcp.Content)
			if !ok {
				data, err := json.Marshal(result.Content)
				if err != nil {
					return gomcp.NewToolResultError(err.Error()), nil
				}
				content = []gomcp.Content{gomcp.TextContent{Type: gomcp.ContentTypeText, Text: string(data)}}
			}
			return &gomcp.CallToolResult{
				Content:           content,
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		}

		content := result.Content
		// Wrap slice results for MCP compliance
		if _, ok := content.([]any); ok {
			content = map[string]any{"items": content}
		}

		// Marshal to JSON text for the tool result
		data, err := json.Marshal(content)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out := gomcp.NewToolResultText(string(data))
		out.IsError = result.IsError
		return out, nil
	}
}
