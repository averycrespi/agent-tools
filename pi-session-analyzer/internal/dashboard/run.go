package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
)

type Options struct {
	Port        int
	NoOpen      bool
	Output      io.Writer
	Ready       func(string)
	OpenBrowser func(context.Context, string) error
}

func Run(ctx context.Context, databasePath string, opts Options) error {
	if opts.Port < 0 || opts.Port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	boundary, err := robound.Open(runCtx, databasePath)
	if err != nil {
		return err
	}
	defer func() { _ = boundary.Close() }()
	if err = validateSchema(runCtx, boundary); err != nil {
		return err
	}
	listener, err := Listen(opts.Port)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	output := opts.Output
	if output == nil {
		output = io.Discard
	}
	url := "http://" + listener.Addr().String()
	_, _ = fmt.Fprintln(output, url)
	if opts.Ready != nil {
		opts.Ready(url)
	}

	server := &http.Server{
		Handler:           NewHandler(boundary),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	if !opts.NoOpen {
		opener := opts.OpenBrowser
		if opener == nil {
			opener = openBrowser
		}
		if openErr := opener(runCtx, url); openErr != nil {
			_, _ = fmt.Fprintf(output, "browser open failed: %v\n", openErr)
		}
	}

	select {
	case <-runCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down dashboard: %w", err)
		}
		err = <-serverErr
	case err = <-serverErr:
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve dashboard: %w", err)
	}
	return nil
}

func validateSchema(ctx context.Context, boundary *robound.Conn) error {
	queryCtx, cancel := robound.WithTimeout(ctx)
	defer cancel()
	rows, err := boundary.QueryContext(queryCtx, `SELECT started_at_unix FROM sessions LIMIT 0`)
	if err != nil {
		return fmt.Errorf("dashboard database schema is out of date; run pi-session-analyzer ingest: %w", err)
	}
	if err = rows.Close(); err != nil {
		return fmt.Errorf("validate dashboard database schema: %w", err)
	}
	return nil
}

func openBrowser(ctx context.Context, url string) error {
	openCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(openCtx, "open", url) //nolint:gosec // The loopback URL is passed as one argument without a shell.
	case "windows":
		cmd = exec.CommandContext(openCtx, "rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // The loopback URL is passed as one argument without a shell.
	default:
		cmd = exec.CommandContext(openCtx, "xdg-open", url) //nolint:gosec // The loopback URL is passed as one argument without a shell.
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	return nil
}
