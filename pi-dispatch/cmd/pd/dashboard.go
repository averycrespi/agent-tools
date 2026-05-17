package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/auth"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/dashboard"
	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Open Pi Dispatch Dashboard",
	Args:  cobra.NoArgs,
	RunE:  runDashboard,
}

func init() {
	dashboardCmd.Flags().String("host", "127.0.0.1", "host to bind; must be loopback")
	dashboardCmd.Flags().Int("port", 8300, "port to bind")
	dashboardCmd.Flags().Bool("no-open", false, "do not open Pi Dispatch Dashboard in a browser")
}

func runDashboard(cmd *cobra.Command, _ []string) error {
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	logf := func(format string, args ...any) {
		dashboardLogf(cmd.ErrOrStderr(), format, args...)
	}
	verboseLogf := func(format string, args ...any) {
		if verbose {
			logf(format, args...)
		}
	}

	logf("validating loopback host=%s", host)
	if err := validateDashboardHost(host); err != nil {
		return err
	}

	tokenPath := auth.TokenPath()
	logf("using auth token path=%s", tokenPath)
	token, err := auth.EnsureToken(tokenPath)
	if err != nil {
		return fmt.Errorf("loading auth token: %w", err)
	}

	dbPath := cfg.DBPath()
	logf("opening database path=%s", dbPath)
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close() //nolint:errcheck

	dash := dashboard.New(st)
	mux := http.NewServeMux()
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dash.Handler()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	logf("mounting routes ui=/dashboard/ api=/dashboard/api/ events=/dashboard/events")
	srv := &http.Server{
		Addr:              addr,
		Handler:           dashboardRequestLogger(cmd.ErrOrStderr(), verbose, auth.MiddlewareWithLog(token, mux, verboseLogf)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := dashboardURL(host, port, token)
	logf("listening addr=%s", addr)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Pi Dispatch Dashboard: %s\n", url)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	if noOpen {
		logf("browser auto-open disabled")
	} else {
		logf("opening browser")
		if err := openDashboardBrowser(pdexec.NewOSRunner(), url); err != nil {
			logf("browser open failed error=%v", err)
			if logger != nil {
				logger.Warn("failed to open browser", "error", err)
			}
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	return waitForDashboardShutdown(
		signalEvents(stop),
		errCh,
		srv.Shutdown,
		logf,
		func() { os.Exit(1) },
	)
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

func waitForDashboardShutdown(signals <-chan struct{}, errCh <-chan error, shutdown func(context.Context) error, logf func(string, ...any), forceExit func()) error {
	select {
	case <-signals:
		logf("shutting down, send Ctrl-C again to force exit")
		go func() {
			<-signals
			logf("forced shutdown")
			forceExit()
		}()
		return shutdown(context.Background())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func dashboardRequestLogger(out io.Writer, verbose bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if verbose || recorder.status >= http.StatusBadRequest {
			dashboardLogf(out, "request method=%s path=%s status=%d duration=%s", r.Method, r.URL.Path, recorder.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

func dashboardLogf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}

func validateDashboardHost(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolving host: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("dashboard host must be loopback: %s", host)
}

func dashboardURL(host string, port int, token string) string {
	urlHost := host
	if host == "127.0.0.1" {
		urlHost = "localhost"
	}
	return fmt.Sprintf("http://%s/dashboard/?token=%s", net.JoinHostPort(urlHost, fmt.Sprintf("%d", port)), token)
}

func openDashboardBrowser(runner pdexec.Runner, url string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	_, err := runner.Start(cmd, url)
	return err
}
