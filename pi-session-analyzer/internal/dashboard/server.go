// Package dashboard serves the analyzer's private loopback-only visual interface.
package dashboard

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
)

//go:embed assets/*
var assets embed.FS

type Handler struct {
	boundary *robound.Conn
	reader   *store.Reader
	now      func() time.Time
}

func NewHandler(boundary *robound.Conn) *Handler {
	handler := &Handler{boundary: boundary, now: time.Now}
	if boundary != nil {
		handler.reader = store.NewReader(
			func(ctx context.Context, query string, args ...any) (store.Rows, error) {
				return boundary.QueryContext(ctx, query, args...)
			},
			func(ctx context.Context, query string, args ...any) store.Row {
				return boundary.QueryRowContext(ctx, query, args...)
			},
		)
	}
	return handler
}

func Listen(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("port must be between 0 and 65535")
	}
	return net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/":
		h.serveAsset(w, "assets/index.html", "text/html; charset=utf-8")
	case "/assets/app.css":
		h.serveAsset(w, "assets/app.css", "text/css; charset=utf-8")
	case "/assets/app.js":
		h.serveAsset(w, "assets/app.js", "text/javascript; charset=utf-8")
	case "/api/overview":
		h.serveOverview(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			switch {
			case strings.HasSuffix(r.URL.Path, "/stream"):
				h.serveSessionStream(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/todo"):
				h.serveTodoDiagnostics(w, r)
				return
			}
		}
		writeError(w, http.StatusNotFound, "route not found")
	}
}

func (h *Handler) serveAsset(w http.ResponseWriter, name, contentType string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "asset unavailable")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func setPrivateHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(robound.MarshalCapped(value))) //nolint:gosec // Values are JSON-encoded and served with a non-HTML content type.
}
