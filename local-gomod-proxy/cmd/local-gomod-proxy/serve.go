package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
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

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/goenv"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/server"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
	"github.com/spf13/cobra"
)

var (
	serveAddr     string
	servePrivate  string
	serveUpstream string
	serveStateDir string
)

// maxConcurrentPrivate caps in-flight `go mod download` subprocesses so a
// runaway client cannot exhaust host resources. Sized for typical go build
// parallelism; most hosts will never reach it.
const maxConcurrentPrivate = 8

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:7070", "address to listen on")
	serveCmd.Flags().StringVar(&servePrivate, "private", "", "GOPRIVATE-style patterns (overrides `go env GOPRIVATE`)")
	serveCmd.Flags().StringVar(&serveUpstream, "upstream", "https://proxy.golang.org", "public upstream proxy URL")
	serveCmd.Flags().StringVar(&serveStateDir, "state-dir", "",
		"directory for TLS cert + credentials (default $XDG_STATE_HOME/local-gomod-proxy)")
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP proxy server",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewOSRunner()
		env, err := goenv.Read(context.Background(), runner)
		if err != nil {
			return fmt.Errorf("reading go env: %w", err)
		}

		if err := server.ValidateLoopbackAddr(serveAddr); err != nil {
			return err
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

		stateDir, err := state.ResolveDir(serveStateDir)
		if err != nil {
			return fmt.Errorf("resolving state dir: %w", err)
		}
		if err := state.EnsureDir(stateDir); err != nil {
			return err
		}
		certPath, keyPath, err := state.LoadOrGenerateCert(stateDir)
		if err != nil {
			return fmt.Errorf("loading cert: %w", err)
		}
		creds, err := state.LoadOrGenerateCredentials(stateDir)
		if err != nil {
			return fmt.Errorf("loading credentials: %w", err)
		}
		fingerprint, err := certFingerprint(certPath)
		if err != nil {
			return fmt.Errorf("reading cert fingerprint: %w", err)
		}

		handler := auth.Middleware(
			server.New(
				router.New(private_),
				private.New(runner),
				public.New(upstream),
				maxConcurrentPrivate,
			),
			creds,
		)

		srv := &http.Server{
			Addr:              serveAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			// Generous WriteTimeout: private .zip artifacts can be many MB
			// and are streamed from disk over a potentially slow upstream.
			WriteTimeout:   5 * time.Minute,
			IdleTimeout:    60 * time.Second,
			MaxHeaderBytes: 16 << 10,
		}

		slog.Info("starting local-gomod-proxy",
			"addr", serveAddr,
			"goprivate", private_,
			"gomodcache", env.GOMODCACHE,
			"goversion", env.GOVERSION,
			"upstream", serveUpstream,
			"state_dir", stateDir,
			"cert_fp", fingerprint)

		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		errCh := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServeTLS(certPath, keyPath); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// certFingerprint returns the first 16 hex chars of the SHA-256 over the
// leaf cert DER. Logged at startup so operators can confirm which cert was
// loaded without touching the key material.
func certFingerprint(certPath string) (string, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", certPath)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])[:16], nil
}
