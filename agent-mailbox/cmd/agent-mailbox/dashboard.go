package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/auth"
	"github.com/averycrespi/agent-tools/agent-mailbox/internal/config"
	"github.com/averycrespi/agent-tools/agent-mailbox/internal/dashboard"
	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	"github.com/spf13/cobra"
)

const dashboardShutdownTimeout = 10 * time.Second

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Serve the HTTP dashboard",
	Args:  cobra.NoArgs,
	RunE:  runDashboard,
}

func init() {
	dashboardCmd.Flags().String("host", "127.0.0.1", "host to bind; must be loopback")
	dashboardCmd.Flags().Int("port", 8500, "port to bind")
	dashboardCmd.Flags().Bool("no-open", false, "do not open the dashboard in a browser")
}

func runDashboard(cmd *cobra.Command, _ []string) error {
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")
	if err := validateDashboardHost(host); err != nil {
		return err
	}
	token, err := auth.EnsureToken(config.TokenPath())
	if err != nil {
		return fmt.Errorf("loading auth token: %w", err)
	}
	st, err := store.Open(activeDBPath())
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close() //nolint:errcheck

	dash := dashboard.New(st)
	mux := http.NewServeMux()
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dash.Handler()))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/dashboard/", http.StatusFound) })
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	srv := &http.Server{Addr: addr, Handler: auth.Middleware(token, mux), ReadHeaderTimeout: 10 * time.Second}
	url := dashboardURL(host, port, token)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Agent Mailbox Dashboard: %s\n", url)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	if !noOpen {
		_ = openBrowser(url)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), dashboardShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return srv.Close()
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
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("dashboard host must be 127.0.0.1, localhost, or ::1: %s", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolving host: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("dashboard host did not resolve: %s", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("dashboard host must resolve only to loopback addresses: %s", host)
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

func openBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		command, args = "xdg-open", []string{url}
	}
	return exec.Command(command, args...).Start() //nolint:gosec // command is selected by platform; URL is generated locally.
}
