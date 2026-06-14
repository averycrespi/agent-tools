package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	osExec "os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/auth"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/dashboard"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/spf13/cobra"
)

const dashboardShutdownTimeout = 10 * time.Second

var (
	openDashboardBrowser  = defaultOpenDashboardBrowser
	lookupDashboardHostIP = net.LookupIP
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Serve the HTTP dashboard",
	Args:  cobra.NoArgs,
	RunE:  runDashboard,
}

func init() {
	dashboardCmd.Flags().String("host", "127.0.0.1", "host to bind; must be loopback")
	dashboardCmd.Flags().Int("port", 8400, "port to bind")
	dashboardCmd.Flags().Bool("no-open", false, "do not open the dashboard in a browser")
}

func runDashboard(cmd *cobra.Command, _ []string) error {
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	if err := validateDashboardHost(host); err != nil {
		return err
	}
	token, err := auth.EnsureToken(auth.TokenPath())
	if err != nil {
		return fmt.Errorf("loading auth token: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer db.Close() //nolint:errcheck

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	srv := &http.Server{Addr: addr, Handler: dashboardMux(db, token), ReadHeaderTimeout: 10 * time.Second}
	url := dashboardURL(host, port, token)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Pi Orchestrator Dashboard: %s\n", url)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	maybeOpenDashboard(noOpen, url)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	return waitForDashboardShutdown(signalEvents(stop), errCh, srv.Shutdown, srv.Close, dashboardShutdownTimeout, func() { os.Exit(1) })
}

func dashboardMux(db *store.Store, token string) http.Handler {
	return dashboard.NewHandler(db, token)
}

func signalEvents(signals <-chan os.Signal) <-chan struct{} {
	events := make(chan struct{})
	go func() {
		for range signals {
			events <- struct{}{}
		}
	}()
	return events
}

func waitForDashboardShutdown(signals <-chan struct{}, errCh <-chan error, shutdown func(context.Context) error, forceClose func() error, timeout time.Duration, forceExit func()) error {
	select {
	case <-signals:
		go func() {
			<-signals
			forceExit()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if closeErr := forceClose(); closeErr != nil {
				return fmt.Errorf("forcing shutdown: %w", closeErr)
			}
		}
		return nil
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

func validateDashboardHost(host string) error {
	ips, err := lookupDashboardHostIP(host)
	if err != nil {
		return fmt.Errorf("resolving host: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("dashboard host must resolve to a loopback address: %s", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("dashboard host must be loopback: %s", host)
		}
	}
	return nil
}

func dashboardURL(host string, port int, token string) string {
	urlHost := host
	if host == "127.0.0.1" {
		urlHost = "localhost"
	}
	return fmt.Sprintf("http://%s/dashboard/?token=%s", net.JoinHostPort(urlHost, fmt.Sprintf("%d", port)), token)
}

func maybeOpenDashboard(noOpen bool, url string) {
	if noOpen {
		return
	}
	_ = openDashboardBrowser(url)
}

func defaultOpenDashboardBrowser(url string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return osExec.Command(cmd, url).Start() //nolint:gosec
}
