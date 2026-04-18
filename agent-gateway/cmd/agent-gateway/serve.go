package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/approval"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/ca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/daemon"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/dashboard"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
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

	// Headless, when true, suppresses the open-browser call regardless of the
	// config value. Tests and CI set this to avoid launching a real browser.
	Headless bool

	// OpenBrowserFn is called with the dashboard URL when the server starts for
	// the first time (first-run) and Headless is false and cfg.Dashboard.OpenBrowser
	// is true. If nil, the default platform-specific opener is used.
	OpenBrowserFn func(url string) error
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
	var headless bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the agent-gateway proxy and dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps := newServeDeps()
			deps.ConfigPath = configPath()
			deps.Headless = headless
			return RunServe(cmd.Context(), deps)
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false, "suppress open-browser on first run (useful for CI and headless servers)")
	return cmd
}

// RunServe binds the proxy and dashboard listeners, starts HTTP servers,
// installs signal handlers, and blocks until ctx is cancelled or a shutdown
// signal (SIGTERM/SIGINT) arrives. Returns nil on clean shutdown.
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

	// 3b. Initialise the secrets store and header injector.
	// Failure to open the secrets store is non-fatal: the injector is omitted and
	// rules with inject blocks will be forwarded with fail-soft behaviour.
	var proxyInjector proxy.Injector
	var inj *inject.Injector // kept for SIGHUP cache invalidation
	secretsStore, secretsErr := secrets.NewStore(db, log)
	if secretsErr != nil {
		log.Warn("secrets store unavailable; header injection disabled", "err", secretsErr)
	} else {
		inj = inject.NewInjector(secretsStore, cfg.Secrets.CacheTTL)
		proxyInjector = &injectAdapter{inj: inj}
	}

	// 3c. Initialise the agents registry and approval broker.
	agentsRegistry, agentsErr := agents.NewRegistry(ctx, db)
	if agentsErr != nil {
		log.Warn("agents registry unavailable", "err", agentsErr)
		agentsRegistry = nil
	}

	// dashBroadcast is set to dashServer.Broadcast after the dashboard server is
	// constructed (below). The closures here capture the variable by reference so
	// they resolve to the real function at call time, after it has been assigned.
	var dashBroadcast func(kind string, data any)

	approvalBroker := approval.New(approval.Opts{
		MaxPending: cfg.Approval.MaxPending,
		Timeout:    cfg.Approval.Timeout,
		OnEvent: func(kind string, data any) {
			if dashBroadcast != nil {
				dashBroadcast(kind, data)
			}
		},
	})

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

	// 6c. Build the audit logger, start the nightly retention pruner, and build
	// the real MITM proxy.
	auditor := audit.NewLogger(db)

	pruneAt, err := audit.ParsePruneAt(cfg.Audit.PruneAt)
	if err != nil {
		return fmt.Errorf("parse audit.prune_at: %w", err)
	}
	retention := time.Duration(cfg.Audit.RetentionDays) * 24 * time.Hour
	go audit.RunPruneLoop(ctx, auditor, log, retention, pruneAt, audit.RealClock{})

	p := proxy.New(proxy.Deps{
		CA:                   authority,
		Registry:             agentsRegistry,
		NoInterceptHosts:     cfg.ProxyBehavior.NoInterceptHosts,
		UpstreamRoundTripper: upstreamRT,
		Rules:                engine,
		Approval:             approvalBroker,
		Injector:             proxyInjector,
		Auditor:              auditor,
		OnRequest: func(entry audit.Entry) {
			if dashBroadcast != nil {
				dashBroadcast("request", entry)
			}
		},
		Logger:            log,
		HandshakeTimeout:  cfg.Timeouts.MITMHandshake,
		ReadHeaderTimeout: cfg.Timeouts.ConnectReadHeader,
		IdleTimeout:       cfg.Timeouts.IdleKeepalive,
		MaxBodyBuffer:     cfg.ProxyBehavior.MaxBodyBuffer,
		BodyBufferTimeout: cfg.Timeouts.BodyBufferRead,
	})

	// Start proxy: Serve blocks on Accept; close proxyLn on ctx.Done to stop it.
	go func() {
		<-ctx.Done()
		_ = proxyLn.Close()
	}()
	go p.Serve(proxyLn)

	// 6d. Build and start the real dashboard server.
	dashServer := dashboard.New(dashboard.Deps{
		AdminTokenPath: paths.AdminTokenFile(),
		Rules:          engine,
		Agents:         agentsRegistry,
		Secrets:        secretsStore,
		Auditor:        auditor,
		Approval:       approvalBroker,
		CAPath:         paths.CACert(),
		Logger:         log,
	})
	// Wire the dashboard SSE broadcast so proxy and approval callbacks fire it.
	dashBroadcast = dashServer.Broadcast

	dashHandler, firstRun := dashServer.Handler()

	dashSrv := &http.Server{Handler: dashHandler}

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

	// 7. Signal ready.
	if d.Ready != nil {
		close(d.Ready)
	}

	dashURL := fmt.Sprintf("http://%s/dashboard/?token=%s", dashLn.Addr(), dashServer.Token())
	log.Info("agent-gateway started",
		"proxy", proxyLn.Addr(),
		"dashboard", dashLn.Addr(),
	)

	// Startup summary: log counts and MITM-eligible hosts.
	{
		agentCount := 0
		if agentsRegistry != nil {
			if list, listErr := agentsRegistry.List(ctx); listErr == nil {
				agentCount = len(list)
			}
		}
		secretCount := 0
		if secretsStore != nil {
			if list, listErr := secretsStore.List(ctx); listErr == nil {
				secretCount = len(list)
			}
		}
		ruleCount := len(engine.Rules())
		mitmHosts := engine.AllRuleHosts()
		log.Info("startup summary",
			"agents", agentCount,
			"secrets", secretCount,
			"rules", ruleCount,
			"mitm_hosts", mitmHosts,
		)
	}

	// Print the authenticated URL on first run (token file was just created).
	if firstRun {
		fmt.Printf("Dashboard: %s\n", dashURL)
	}

	// Open browser if configured and not headless.
	if firstRun && !d.Headless && cfg.Dashboard.OpenBrowser {
		openFn := d.OpenBrowserFn
		if openFn == nil {
			openFn = openBrowser
		}
		if err := openFn(dashURL); err != nil {
			log.Warn("failed to open browser", "err", err)
		}
	}

	// 8. Install signal handlers.
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
				log.Info("received SIGHUP; reloading")
				if reloadErr := engine.Reload(); reloadErr != nil {
					log.Error("rules reload failed", "err", reloadErr)
					// Previous ruleset stays live — keep serving.
				} else {
					log.Info("rules reloaded")
				}
				if inj != nil {
					inj.InvalidateCache()
					log.Info("injector cache invalidated")
				}
				if agentsRegistry != nil {
					if reloadErr := agentsRegistry.ReloadFromDB(ctx); reloadErr != nil {
						log.Warn("agents registry reload failed", "err", reloadErr)
					} else {
						log.Info("agents registry reloaded")
					}
				}
				if reloadErr := dashServer.ReloadToken(); reloadErr != nil {
					log.Warn("admin token reload failed", "err", reloadErr)
				} else {
					log.Info("admin token reloaded")
				}
			case syscall.SIGTERM, syscall.SIGINT:
				log.Info("received signal; shutting down", "signal", sig)
				return shutdown(log, dashSrv)
			}

		case err := <-dashErr:
			return fmt.Errorf("dashboard server error: %w", err)
		}
	}
}

// openBrowser launches the default browser for url using the platform's
// native open command.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	default:
		return errors.New("open browser: unsupported platform")
	}
}

// injectAdapter adapts *inject.Injector to the proxy.Injector interface by
// extracting the context from req.Context() rather than accepting it as a
// separate parameter. This keeps the proxy.Injector interface simple.
type injectAdapter struct {
	inj *inject.Injector
}

func (a *injectAdapter) Apply(req *http.Request, rule *rules.Rule, agent string) (inject.InjectionStatus, string, error) {
	return a.inj.Apply(req.Context(), req, rule, agent)
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
