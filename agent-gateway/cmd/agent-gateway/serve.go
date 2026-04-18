package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// serveDeps holds injectable dependencies for RunServe. Tests supply custom
// channels for synchronisation; production code uses newServeDeps().
type serveDeps struct {
	// ConfigPath is the path to the config file to load. If empty, RunServe
	// falls back to paths.ConfigFile() (the XDG default location).
	ConfigPath string

	// Logger is the structured logger used by the server.
	Logger *slog.Logger

	// Ready is closed (or receives a value) once both listeners are bound and
	// the HTTP servers are ready to accept connections. Tests block on this.
	Ready chan struct{}

	// ProxyAddr receives the bound proxy address (host:port) after startup.
	// Must be buffered (cap >= 1) if non-nil.
	ProxyAddr chan string

	// DashboardAddr receives the bound dashboard address (host:port) after startup.
	// Must be buffered (cap >= 1) if non-nil.
	DashboardAddr chan string
}

// newServeDeps returns production-ready defaults. Tests may override fields.
func newServeDeps() serveDeps {
	return serveDeps{
		Logger: slog.Default(),
		// Ready is nil by default; RunServe checks before sending.
	}
}

// newServeCmd returns a cobra.Command that runs RunServe.
func newServeCmd(configPath func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the agent-gateway proxy and dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps := newServeDeps()
			deps.ConfigPath = configPath()
			return RunServe(cmd.Context(), deps)
		},
	}
}

// RunServe binds the proxy and dashboard listeners, starts placeholder HTTP
// servers, installs signal handlers, and blocks until ctx is cancelled or a
// shutdown signal (SIGTERM/SIGINT) arrives. Returns nil on clean shutdown.
func RunServe(ctx context.Context, d serveDeps) error {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}

	// 1. Load config.
	cfgPath := d.ConfigPath
	if cfgPath == "" {
		cfgPath = paths.ConfigFile()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Ensure config, data, and rules directories exist.
	if err := os.MkdirAll(paths.ConfigDir(), 0o750); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(paths.DataDir(), 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(paths.RulesDir(), 0o750); err != nil {
		return fmt.Errorf("create rules dir: %w", err)
	}

	// 2b. Initialise the rules engine (0 rules is valid).
	engine, err := rules.NewEngine(paths.RulesDir())
	if err != nil {
		return fmt.Errorf("init rules engine: %w", err)
	}

	// 3. Open database.
	db, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Warn("failed to close state.db", "err", err)
		}
	}()

	// 4. Acquire PID file.
	pidHandle, err := daemon.Acquire(paths.PIDFile())
	if err != nil {
		return fmt.Errorf("acquire pid file: %w", err)
	}
	defer func() {
		if releaseErr := pidHandle.Release(); releaseErr != nil {
			log.Warn("failed to release pid file", "err", releaseErr)
		}
	}()

	// 5. Bind proxy listener.
	proxyLn, err := net.Listen("tcp", cfg.Proxy.Listen)
	if err != nil {
		return fmt.Errorf("bind proxy listener: %w", err)
	}
	defer func() { _ = proxyLn.Close() }()

	// 5b. Bind dashboard listener.
	dashLn, err := net.Listen("tcp", cfg.Dashboard.Listen)
	if err != nil {
		return fmt.Errorf("bind dashboard listener: %w", err)
	}
	defer func() { _ = dashLn.Close() }()

	// 6a. Load or generate root CA.
	authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	if err != nil {
		return fmt.Errorf("load or generate CA: %w", err)
	}
	authority.Start(ctx)

	// 6b. Build upstream RoundTripper with config-driven timeouts.
	upstreamRT := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12}, //nolint:gosec
		DialContext: (&net.Dialer{
			Timeout: cfg.Timeouts.UpstreamDial,
		}).DialContext,
		TLSHandshakeTimeout: cfg.Timeouts.UpstreamTLS,
		IdleConnTimeout:     cfg.Timeouts.UpstreamIdleKeepalive,
	}

	// 6c. Build the real MITM proxy.
	p := proxy.New(proxy.Deps{
		CA:                   authority,
		UpstreamRoundTripper: upstreamRT,
		Rules:                engine,
		Logger:               log,
		HandshakeTimeout:     cfg.Timeouts.MITMHandshake,
		ReadHeaderTimeout:    cfg.Timeouts.ConnectReadHeader,
		IdleTimeout:          cfg.Timeouts.IdleKeepalive,
	})

	// Start proxy: Serve blocks on Accept; close proxyLn on ctx.Done to stop it.
	go func() {
		<-ctx.Done()
		_ = proxyLn.Close()
	}()
	go p.Serve(proxyLn)

	// 6d. Dashboard placeholder HTTP server.
	dashSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "hello")
		}),
	}

	dashErr := make(chan error, 1)
	go func() {
		if err := dashSrv.Serve(dashLn); err != nil && err != http.ErrServerClosed {
			dashErr <- err
		}
	}()

	// Send bound addresses to callers that want them.
	if d.ProxyAddr != nil {
		d.ProxyAddr <- proxyLn.Addr().String()
	}
	if d.DashboardAddr != nil {
		d.DashboardAddr <- dashLn.Addr().String()
	}

	// 8. Signal ready.
	if d.Ready != nil {
		close(d.Ready)
	}

	log.Info("agent-gateway started",
		"proxy", proxyLn.Addr(),
		"dashboard", dashLn.Addr(),
	)

	// 7. Install signal handlers.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	// Block until shutdown is requested.
	for {
		select {
		case <-ctx.Done():
			log.Info("context cancelled; shutting down")
			return shutdown(log, dashSrv)

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				log.Info("received SIGHUP; reloading rules")
				if reloadErr := engine.Reload(); reloadErr != nil {
					log.Error("rules reload failed", "err", reloadErr)
					// Previous ruleset stays live — keep serving.
				} else {
					log.Info("rules reloaded")
				}
				// TODO(Task 22): secrets.InvalidateCache()
				// TODO(Task 26): agents.ReloadFromDB()
			case syscall.SIGTERM, syscall.SIGINT:
				log.Info("received signal; shutting down", "signal", sig)
				return shutdown(log, dashSrv)
			}

		case err := <-dashErr:
			return fmt.Errorf("dashboard server error: %w", err)
		}
	}
}

// shutdown gracefully shuts down both HTTP servers with a 30-second timeout.
func shutdown(log *slog.Logger, servers ...*http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var errs []error
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Warn("server shutdown error", "err", err)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
