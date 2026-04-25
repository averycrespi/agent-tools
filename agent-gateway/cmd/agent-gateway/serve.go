package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/netguard"
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

	// OpenBrowserFn is called with the dashboard URL on every serve start
	// when Headless is false and cfg.Dashboard.OpenBrowser is true. If nil,
	// the default platform-specific opener is used.
	OpenBrowserFn func(url string) error

	// NewSecretsStoreFn constructs the secrets store. If nil, secrets.NewStore
	// is used. Tests override this to inject failures.
	NewSecretsStoreFn func(*sql.DB, *slog.Logger) (secrets.Store, error)

	// Stdout is the writer for human-readable startup lines (paths, URLs).
	// If nil, os.Stdout is used. Tests inject a buffer to capture output.
	Stdout io.Writer
}

// newServeDeps returns production-ready defaults. Tests may override fields.
func newServeDeps() serveDeps {
	return serveDeps{
		Logger:            slog.Default(),
		NewSecretsStoreFn: secrets.NewStore,
		Stdout:            os.Stdout,
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
	// Tighten the process umask before any MkdirAll / Open / file creation
	// below. The daemon handles agent tokens (argon2id hashes), AES-256-GCM
	// secret ciphertexts, the admin token, the CA private key, and the audit
	// log — none of which should ever land on disk group- or world-readable.
	// Explicit os.Chmod and 0o600 WriteFile calls already cover the files we
	// know about; this umask is defense-in-depth for future code paths (and
	// library-created sidecar files like SQLite's -wal/-shm) that forget an
	// explicit mode. syscall.Umask returns the previous value; we discard it
	// since RunServe is the main daemon entry point and has no caller to
	// restore to.
	_ = syscall.Umask(0o077)

	log := d.Logger
	if log == nil {
		log = slog.Default()
	}

	stdout := d.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	// 1. Load config.
	cfgPath := d.ConfigPath
	if cfgPath == "" {
		cfgPath = paths.ConfigFile()
	}
	cfg, cfgWarnings, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, w := range cfgWarnings {
		log.Warn(w)
	}

	// 2. Ensure config, data, and rules directories exist, and verify that
	// each one is owned by the current user with mode no wider than 0o700.
	// The self-check after MkdirAll exists because MkdirAll is a no-op on an
	// existing directory — upgraded installs that were first created at
	// 0o750 would otherwise silently stay wide open after the MkdirAll mode
	// was tightened. See paths.CheckOwnerAndMode for the full rationale.
	if err := os.MkdirAll(paths.ConfigDir(), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(paths.RulesDir(), 0o700); err != nil {
		return fmt.Errorf("create rules dir: %w", err)
	}
	for _, d := range []string{paths.ConfigDir(), paths.DataDir(), paths.RulesDir()} {
		if err := paths.CheckOwnerAndMode(d, 0o700); err != nil {
			return fmt.Errorf("insecure xdg directory: %w", err)
		}
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

	// 3a. Record a sha256 of config.hcl in the meta table so that the reload
	// command (Task 4.4) can detect config drift between daemon start and now.
	cfgHash, err := sha256File(cfgPath)
	if err != nil {
		return fmt.Errorf("hash config file: %w", err)
	}
	if err := store.PutMeta(db, "config_hash", cfgHash); err != nil {
		return fmt.Errorf("record config hash: %w", err)
	}

	// 3b. Initialise the secrets store and header injector. Failure is fatal:
	// running with no injector silently leaks sandbox dummy tokens through rules
	// that were meant to swap in real credentials, indistinguishable from "no
	// rule matched" in the audit log.
	newSecretsStoreFn := d.NewSecretsStoreFn
	if newSecretsStoreFn == nil {
		newSecretsStoreFn = secrets.NewStore
	}
	secretsStore, err := newSecretsStoreFn(db, log)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr,
			"agent-gateway: secrets store unavailable: %v\n"+
				"  The daemon requires a working secrets store to inject credentials.\n"+
				"  If the keychain is unavailable, ensure the file fallback path is readable.\n",
			err,
		)
		return fmt.Errorf("secrets store unavailable: %w", err)
	}
	inj := inject.NewInjector(secretsStore, cfg.Secrets.CacheTTL)
	proxyInjector := &injectAdapter{inj: inj}

	// 3b.1. Surface coverage warnings: rules that reference ${secrets.X} but
	// whose match.host pattern may not be covered by the secret's
	// allowed_hosts. Non-fatal; the runtime will still enforce scope on each
	// request via ErrSecretHostScopeViolation.
	for _, w := range warnSecretCoverage(ctx, engine, secretsStore) {
		log.Warn(w)
	}

	// 3b.2. Surface no-intercept overlap warnings: rules whose match.host
	// overlaps a no_intercept_hosts entry, meaning the proxy tunnels those
	// connections raw and the rule will never fire. Non-fatal; the operator
	// may have intentionally added both entries, but silent dead code is the
	// common footgun.
	for _, w := range warnNoInterceptOverlap(engine, cfg.ProxyBehavior.NoInterceptHosts) {
		log.Warn(w)
	}

	// 3c. Initialise the agents registry and approval broker.
	// Registry failure is fatal: without it, the proxy cannot authenticate
	// CONNECT requests and would silently accept any caller, ignore
	// no_intercept_hosts, and resolve only global secrets. Fail closed
	// rather than boot into that degraded state.
	agentsRegistry, err := agents.NewRegistry(ctx, db)
	if err != nil {
		return fmt.Errorf("initialise agents registry: %w", err)
	}

	// dashBroadcast is set to dashServer.Broadcast after the dashboard server is
	// constructed (below). The closures here capture the variable by reference so
	// they resolve to the real function at call time, after it has been assigned.
	var dashBroadcast func(kind dashboard.EventKind, data any)

	approvalBroker := approval.New(approval.Opts{
		MaxPending:         cfg.Approval.MaxPending,
		MaxPendingPerAgent: cfg.Approval.MaxPendingPerAgent,
		Timeout:            cfg.Approval.Timeout,
		OnEvent: func(kind string, data any) {
			if dashBroadcast != nil {
				dashBroadcast(dashboard.EventKind(kind), data)
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
	// WHY: the DialContext is wrapped with netguard to block SSRF and IMDS
	// access before any byte reaches the network. DNS resolution happens inside
	// the guard (resolve → check → dial) so hostname-based SSRF (e.g.
	// "metadata.google.internal") is caught the same way as a literal IP.
	upstreamDialer := &net.Dialer{Timeout: cfg.Timeouts.UpstreamDial}
	upstreamRT := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     upstreamTLSConfig(),
		DialContext:         netguard.DialContext(upstreamDialer, cfg.ProxyBehavior.AllowPrivateUpstream),
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
				dashBroadcast(dashboard.EventRequest, entry)
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

	dashHandler := dashServer.Handler()

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

	// Paths for operator debugging (systemd/launchd stdout picks these up).
	_, _ = fmt.Fprintf(stdout, "config:    %s\n", paths.ConfigFile())
	_, _ = fmt.Fprintf(stdout, "state_db:  %s\n", paths.StateDB())
	_, _ = fmt.Fprintf(stdout, "ca_cert:   %s\n", paths.CACert())
	_, _ = fmt.Fprintf(stdout, "pid_file:  %s\n", paths.PIDFile())
	log.Info("paths",
		"config", paths.ConfigFile(),
		"state_db", paths.StateDB(),
		"ca_cert", paths.CACert(),
		"pid_file", paths.PIDFile(),
	)

	log.Info("agent-gateway started",
		"proxy", proxyLn.Addr(),
		"dashboard", dashLn.Addr(),
	)

	// Startup summary: log counts and MITM-eligible hosts.
	{
		agentCount := 0
		if list, listErr := agentsRegistry.List(ctx); listErr == nil {
			agentCount = len(list)
		}
		secretCount := 0
		if list, listErr := secretsStore.List(ctx); listErr == nil {
			secretCount = len(list)
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

	// Print the authenticated dashboard URL on every serve start.
	_, _ = fmt.Fprintf(stdout, "Dashboard: %s\n", dashURL)

	// Open browser if configured and not headless.
	if !d.Headless && cfg.Dashboard.OpenBrowser {
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
			log.Info("context cancelled; shutting down, send signal again to force exit")
			go forceExitOnSecondSignal(sigCh, log)
			return shutdown(log, dashSrv)

		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				// WHY reload order — agents → rules → injector → secrets → admin → CA:
				// 1. Agents first: ensures the new token hash is live before any
				//    subsequent step could authenticate a request under the new rules.
				//    A partial-failure reload must not accept a new agent token while
				//    still evaluating under the old rule set.
				// 2. Rules after agents: the new ruleset is evaluated against the
				//    already-refreshed agent identity.
				// 3. Injector cache cleared after rules: stale decrypted secrets from
				//    the previous rule epoch are flushed before the new rules run.
				// 4. Secrets coverage warning after rules + injector: re-checks
				//    coverage against the current (post-reload) ruleset.
				// 5. Admin token after secrets: dashboard auth is updated last among
				//    the data-plane components so the UI doesn't briefly lose access
				//    while upstream configs are still refreshing.
				// 6. CA last: leaf cache is cleared after all rule/agent/secret state
				//    is consistent; in-flight TLS handshakes on old leaves complete
				//    normally via the atomic.Pointer snapshot.
				log.Info("received SIGHUP; reloading")
				if reloadErr := agentsRegistry.ReloadFromDB(ctx); reloadErr != nil {
					log.Warn("agents registry reload failed", "err", reloadErr)
				} else {
					log.Info("agents registry reloaded")
				}
				if reloadErr := engine.Reload(); reloadErr != nil {
					log.Error("rules reload failed", "err", reloadErr)
					// Previous ruleset stays live — keep serving.
				} else {
					log.Info("rules reloaded")
				}
				inj.InvalidateCache()
				log.Info("injector cache invalidated")
				// Re-check secret coverage: either the ruleset or a secret's
				// allowed_hosts may have changed since the last reload.
				for _, w := range warnSecretCoverage(ctx, engine, secretsStore) {
					log.Warn(w)
				}
				// Re-check no-intercept overlap: rule changes may introduce
				// or resolve shadows against the (config-static) no_intercept_hosts.
				for _, w := range warnNoInterceptOverlap(engine, cfg.ProxyBehavior.NoInterceptHosts) {
					log.Warn(w)
				}
				if reloadErr := dashServer.ReloadToken(); reloadErr != nil {
					log.Warn("admin token reload failed", "err", reloadErr)
				} else {
					log.Info("admin token reloaded")
				}
				if reloadErr := authority.Reload(); reloadErr != nil {
					log.Warn("CA reload failed; previous CA stays live", "err", reloadErr)
				} else {
					log.Info("CA reloaded; leaf cache cleared")
				}
			case syscall.SIGTERM, syscall.SIGINT:
				log.Info("received signal; shutting down, send again to force exit", "signal", sig)
				go forceExitOnSecondSignal(sigCh, log)
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

// upstreamTLSConfig returns the *tls.Config applied to the upstream
// RoundTripper. Extracted so tests can assert on the minimum TLS version
// without spinning up a full server.
func upstreamTLSConfig() *tls.Config {
	// WHY: VersionTLS13 drops TLS 1.0/1.1/1.2 cipher rollback attack paths.
	// The upstream transport talks to internet servers we do not control;
	// pinning to TLS 1.3 eliminates downgrade negotiation entirely.
	return &tls.Config{MinVersion: tls.VersionTLS13} //nolint:gosec
}

// injectAdapter adapts *inject.Injector to the proxy.Injector interface by
// extracting the context from req.Context() rather than accepting it as a
// separate parameter. This keeps the proxy.Injector interface simple.
type injectAdapter struct {
	inj *inject.Injector
}

func (a *injectAdapter) Apply(req *http.Request, rule *rules.Rule, agent, host string) (inject.InjectionStatus, string, error) {
	return a.inj.Apply(req.Context(), req, rule, agent, host)
}

// forceExitOnSecondSignal blocks on sigCh after graceful shutdown has begun.
// If a second SIGINT/SIGTERM arrives before shutdown completes, the process
// exits immediately with status 1.
func forceExitOnSecondSignal(sigCh <-chan os.Signal, log *slog.Logger) {
	for sig := range sigCh {
		if sig == syscall.SIGINT || sig == syscall.SIGTERM {
			log.Warn("forced shutdown", "signal", sig)
			os.Exit(1)
		}
	}
}

// sha256File returns the hex-encoded sha256 of the file's contents.
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
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
