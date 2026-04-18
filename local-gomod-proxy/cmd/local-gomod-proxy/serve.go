package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/goenv"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/server"
	"github.com/spf13/cobra"
)

var (
	serveAddr     string
	servePrivate  string
	serveUpstream string
)

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:7070", "address to listen on")
	serveCmd.Flags().StringVar(&servePrivate, "private", "", "GOPRIVATE-style patterns (overrides `go env GOPRIVATE`)")
	serveCmd.Flags().StringVar(&serveUpstream, "upstream", "https://proxy.golang.org", "public upstream proxy URL")
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP proxy server",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewOSRunner()
		env, err := goenv.Read(runner)
		if err != nil {
			return fmt.Errorf("reading go env: %w", err)
		}

		private_ := servePrivate
		if private_ == "" {
			private_ = env.GOPRIVATE
		}
		if private_ == "" {
			return errors.New("GOPRIVATE is not set; run `go env -w GOPRIVATE=github.com/your-org/*` on the host, " +
				"or pass --private explicitly; with no private patterns, the proxy has no work to do")
		}
		if env.GOMODCACHE == "" {
			return errors.New("GOMODCACHE is empty; ensure the host's go toolchain is configured")
		}
		if strings.HasPrefix(env.GOVERSION, "go1.1") || env.GOVERSION == "go1.20" {
			slog.Warn("host go version is older than 1.21; modules using the 'toolchain' directive may fail",
				"goversion", env.GOVERSION)
		}

		upstream, err := url.Parse(serveUpstream)
		if err != nil {
			return fmt.Errorf("parsing upstream URL: %w", err)
		}

		handler := server.New(
			router.New(private_),
			private.New(runner),
			public.New(upstream),
		)

		srv := &http.Server{
			Addr:              serveAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}

		slog.Info("starting local-gomod-proxy",
			"addr", serveAddr,
			"goprivate", private_,
			"gomodcache", env.GOMODCACHE,
			"goversion", env.GOVERSION,
			"upstream", serveUpstream)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		errCh := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()

		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		case err := <-errCh:
			return err
		}
	},
}
