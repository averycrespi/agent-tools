package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/egress-broker/internal/audit"
	"github.com/averycrespi/agent-tools/egress-broker/internal/auth"
	"github.com/averycrespi/agent-tools/egress-broker/internal/ca"
	"github.com/averycrespi/agent-tools/egress-broker/internal/config"
	"github.com/averycrespi/agent-tools/egress-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/egress-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/egress-broker/internal/netguard"
	"github.com/averycrespi/agent-tools/egress-broker/internal/paths"
	"github.com/averycrespi/agent-tools/egress-broker/internal/proxy"
	"github.com/averycrespi/agent-tools/egress-broker/internal/rules"
)

// forceExit is a var so tests can observe a shutdown that overran its
// deadline without terminating the test binary.
var forceExit = os.Exit

// shutdownTimeout bounds graceful shutdown of the HTTP servers.
const shutdownTimeout = 10 * time.Second

// pruneInterval is how often expired audit rows are removed.
const pruneInterval = 24 * time.Hour

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the forward proxy and the dashboard",
	Long: "Runs in the foreground. Supervise it with launchd; reload rules with\n" +
		"`launchctl kill HUP` rather than restarting, since restarting points every\n" +
		"sandbox at a dead socket for the duration.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runServe(cmd.Context(), cmd.OutOrStdout())
	},
}

// stack holds everything a running proxy needs, so startup can fail before
// anything binds a port.
type stack struct {
	cfg       config.Config
	log       *slog.Logger
	rules     *rules.Store
	authority *ca.Authority
	resolver  *credentials.Resolver
	token     string
	envNames  []string
	audit     *audit.Logger
}

func runServe(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	st, err := buildStack(out)
	if err != nil {
		return err
	}

	proxyServer, proxyLn, err := startProxy(st)
	if err != nil {
		return err
	}
	healthServer, healthLn, err := startDashboard(st)
	if err != nil {
		_ = proxyServer.Close()
		return err
	}

	st.log.Info("egress-broker listening",
		"proxy", proxyLn.Addr().String(),
		"dashboard", healthLn.Addr().String(),
		"ca", paths.CACert())
	for _, name := range st.envNames {
		st.log.Warn("credential is sourced from the process environment rather than the keychain",
			"credential", name)
	}

	pruneCtx, stopPruning := context.WithCancel(ctx)
	defer stopPruning()
	st.audit.StartPruning(pruneCtx,
		time.Duration(st.cfg.Audit.RetentionDays)*24*time.Hour,
		pruneInterval, time.Now,
		func(err error) { st.log.Error("pruning the audit log failed", "error", err) })

	return serveEventLoop(ctx, st, proxyServer, proxyLn, healthServer, healthLn)
}

func buildStack(out interface{ Write([]byte) (int, error) }) (*stack, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}

	log := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: logLevel(cfg.Log.Level)}))

	rulesPath, doc, err := config.LoadRulesForConfig(configPath(), cfg)
	if err != nil {
		return nil, err
	}
	store, err := rules.NewStore(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rulesPath, err)
	}

	authority, err := ca.LoadOrGenerate(paths.CAKey(), paths.CACert())
	if err != nil {
		return nil, err
	}

	token, err := auth.EnsureToken(auth.TokenPath())
	if err != nil {
		return nil, err
	}

	envSpecs := make(map[string]credentials.EnvSpec, len(cfg.EnvCredentials))
	for name, ec := range cfg.EnvCredentials {
		envSpecs[name] = credentials.EnvSpec{Var: ec.Var, Hosts: ec.Hosts}
	}
	env := credentials.NewEnv(envSpecs)

	// Keychain first: an operator who has stored a credential properly should
	// not have it shadowed by a stale environment entry.
	resolver := credentials.New(credentials.NewKeychain(), env)

	// Warn about coverage gaps at load, where an operator can act on them,
	// rather than leaving them to surface as a 403 an agent hits later.
	scopes := make(map[string]rules.CredentialScope, len(envSpecs))
	for name, spec := range envSpecs {
		scopes[name] = rules.CredentialScope{Name: name, Hosts: spec.Hosts}
	}
	for _, w := range rules.CheckCredentialCoverage(doc, scopes) {
		log.Warn("credential host binding may not cover this rule", "detail", w.String())
	}

	auditLog, err := audit.Open(cfg.Audit.Path)
	if err != nil {
		return nil, err
	}

	return &stack{
		cfg: cfg, log: log, rules: store, authority: authority,
		resolver: resolver, token: token, envNames: env.Names(), audit: auditLog,
	}, nil
}

// testAllowEnv names exact "host:port" targets that skip the address guard.
//
// It exists only so the e2e suite can reach a mock upstream, which necessarily
// listens on loopback — an address the guard refuses by design. Setting it in
// production disables SSRF protection for the named targets, which is why it
// is an environment variable rather than a config field and why every entry is
// logged as a warning at startup.
const testAllowEnv = "EGRESS_BROKER_TEST_ALLOW_ADDRS"

func newProxy(st *stack) *proxy.Proxy {
	dialer := netguard.New(proxy.BaseDialer())

	if raw := os.Getenv(testAllowEnv); raw != "" {
		targets := strings.Split(raw, ",")
		for i := range targets {
			targets[i] = strings.TrimSpace(targets[i])
		}
		dialer.SetExemptions(targets)
		st.log.Warn("address guard disabled for specific targets by "+testAllowEnv+
			"; this is for tests only and must not be set in production",
			"targets", strings.Join(targets, ","))
	}

	return proxy.New(proxy.Options{
		Rules:     st.rules,
		Authority: st.authority,
		Resolver:  st.resolver,
		Dialer:    dialer,
		Audit:     audit.NewSink(st.audit, st.log),
		Token:     st.token,
		Logger:    st.log,
	})
}

func startProxy(st *stack) (*http.Server, net.Listener, error) {
	if err := config.ValidateLoopback(st.cfg.Proxy); err != nil {
		return nil, nil, fmt.Errorf("proxy listener: %w", err)
	}
	ln, err := net.Listen("tcp", st.cfg.Proxy.Addr())
	if err != nil {
		return nil, nil, fmt.Errorf("binding the proxy listener on %s: %w", st.cfg.Proxy.Addr(), err)
	}

	server := &http.Server{
		Handler: newProxy(st),
		// ReadHeaderTimeout bounds how long a client may hold a connection
		// without stating its intent. No overall ReadTimeout: a CONNECT is
		// hijacked and relayed for as long as the tunnel lives.
		ReadHeaderTimeout: proxy.DefaultHeaderTimeout,
	}
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			st.log.Error("proxy listener stopped", "error", err)
		}
	}()
	return server, ln, nil
}

// startDashboard binds the dashboard listener.
func startDashboard(st *stack) (*http.Server, net.Listener, error) {
	if err := config.ValidateLoopback(st.cfg.Dashboard); err != nil {
		return nil, nil, fmt.Errorf("dashboard listener: %w", err)
	}
	ln, err := net.Listen("tcp", st.cfg.Dashboard.Addr())
	if err != nil {
		return nil, nil, fmt.Errorf("binding the dashboard listener on %s: %w", st.cfg.Dashboard.Addr(), err)
	}

	board := dashboard.New(st.audit, st.rules, st.credentialLister(), st.authority, st.token, st.log)
	server := &http.Server{Handler: board.Handler(), ReadHeaderTimeout: proxy.DefaultHeaderTimeout}
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			st.log.Error("dashboard listener stopped", "error", err)
		}
	}()
	return server, ln, nil
}

// serveEventLoop blocks until a termination signal, handling SIGHUP reloads.
func serveEventLoop(ctx context.Context, st *stack, servers ...any) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	for {
		select {
		case <-ctx.Done():
			return shutdownAll(st, servers...)
		case sig := <-sigs:
			switch sig {
			case syscall.SIGHUP:
				st.reload()
			default:
				st.log.Info("shutting down", "signal", sig.String())
				return shutdownAll(st, servers...)
			}
		}
	}
}

// reload re-reads rules and the CA without restarting.
//
// Each step keeps its previous state on failure. A typo in rules.json must not
// take the sandbox's network down, which is the whole reason the kill switch
// is a rules edit rather than a process kill (D15).
func (st *stack) reload() {
	cfg, err := config.Load(configPath())
	if err != nil {
		st.log.Error("reload: config unchanged", "error", err)
		return
	}

	rulesPath, doc, err := config.LoadRulesForConfig(configPath(), cfg)
	if err != nil {
		st.log.Error("reload: rules unchanged", "path", rulesPath, "error", err)
		return
	}
	if err := st.rules.Reload(doc); err != nil {
		st.log.Error("reload: rules unchanged, previous ruleset still serving", "path", rulesPath, "error", err)
		return
	}

	if err := st.authority.Reload(); err != nil {
		st.log.Error("reload: CA unchanged", "error", err)
	}

	// Drop cached credentials so a just-rotated secret takes effect now rather
	// than after the TTL.
	st.resolver.ClearCache()

	st.log.Info("reloaded", "rules", rulesPath, "count", len(doc.Rules), "fallthrough", doc.Fallthrough)
}

func shutdownAll(st *stack, servers ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, s := range servers {
			srv, ok := s.(*http.Server)
			if !ok {
				continue
			}
			if err := srv.Shutdown(ctx); err != nil {
				st.log.Warn("shutdown did not complete cleanly", "error", err)
			}
		}
	}()

	select {
	case <-done:
		if err := st.audit.Close(); err != nil {
			st.log.Warn("closing the audit log", "error", err)
		}
		return nil
	case <-ctx.Done():
		// http.Server.Shutdown has no visibility into hijacked CONNECT
		// relays, so a busy tunnel can outlive the window. Exiting is
		// correct: the alternative is a process that never stops, which
		// launchd would then never restart.
		st.log.Warn("shutdown deadline exceeded with tunnels still open; exiting")
		forceExit(1)
		return nil
	}
}

// credentialLister exposes credential names and bindings to the dashboard.
// It is built from configuration rather than from the resolver, so no code
// path can hand a value to the dashboard even by accident.
func (st *stack) credentialLister() dashboard.CredentialLister {
	infos := make([]dashboard.CredentialInfo, 0, len(st.cfg.EnvCredentials))
	for name, ec := range st.cfg.EnvCredentials {
		infos = append(infos, dashboard.CredentialInfo{
			Name: name, Source: "env_credentials", Hosts: ec.Hosts,
		})
	}
	for _, name := range st.rules.Engine().ReferencedCredentials() {
		if _, isEnv := st.cfg.EnvCredentials[name]; isEnv {
			continue
		}
		infos = append(infos, dashboard.CredentialInfo{Name: name, Source: "keychain"})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return staticCredentialLister(infos)
}

type staticCredentialLister []dashboard.CredentialInfo

func (s staticCredentialLister) List() []dashboard.CredentialInfo { return s }

func logLevel(level string) slog.Level {
	switch level {
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

func init() {
	rootCmd.AddCommand(serveCmd)
}
