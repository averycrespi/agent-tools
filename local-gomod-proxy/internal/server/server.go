package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
)

// New returns an http.Handler implementing the Go module proxy protocol.
// Routes private modules through the PrivateFetcher and public modules through
// the PublicFetcher.
//
// maxConcurrentPrivate bounds the number of in-flight private fetches so a
// runaway client cannot fork unbounded `go mod download` subprocesses on the
// host. Requests over the cap block until a slot is free or the request
// context is cancelled.
func New(r *router.Router, priv *private.Fetcher, pub *public.Fetcher, maxConcurrentPrivate int) http.Handler {
	sem := make(chan struct{}, maxConcurrentPrivate)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		parsed, err := private.ParseRequest(req.URL.Path)
		if err != nil {
			slog.Info("bad request", "path", req.URL.Path, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if r.IsPrivate(parsed.Module) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-req.Context().Done():
				return
			}

			slog.Info("serving private", "module", parsed.Module, "version", parsed.Version)
			if err := priv.Serve(w, req, parsed); err != nil {
				if errors.Is(err, private.ErrResponseCommitted) {
					slog.Warn("mid-stream failure after headers committed", "module", parsed.Module, "err", err)
					return
				}
				if errors.Is(err, private.ErrModuleNotFound) {
					slog.Info("private module not found", "module", parsed.Module, "err", err)
					http.Error(w, "module not found", http.StatusNotFound)
					return
				}
				slog.Error("private fetcher failed", "module", parsed.Module, "err", err)
				http.Error(w, "upstream error", http.StatusBadGateway)
			}
			return
		}

		slog.Info("serving public", "module", parsed.Module, "version", parsed.Version)
		pub.ServeHTTP(w, req)
	})
}
