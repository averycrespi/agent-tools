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

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/auth"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/detect"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
)

//go:embed assets/*
var assets embed.FS

type Handler struct {
	boundary      *robound.Conn
	reader        *store.Reader
	now           func() time.Time
	detectorNames []string
}

func NewHandler(boundary *robound.Conn) *Handler {
	handler := &Handler{boundary: boundary, now: time.Now}
	for _, detector := range detect.Registry() {
		handler.detectorNames = append(handler.detectorNames, detector.Name)
	}
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
	case "/assets/state.js":
		h.serveAsset(w, "assets/state.js", "text/javascript; charset=utf-8")
	case "/assets/view-model.js":
		h.serveAsset(w, "assets/view-model.js", "text/javascript; charset=utf-8")
	case "/assets/favicon.svg":
		h.serveAsset(w, "assets/favicon.svg", "image/svg+xml")
	case "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case "/api/overview":
		h.serveOverview(w, r)
	case "/api/overview/signals":
		h.serveOverviewSignals(w, r)
	case "/api/sessions":
		h.serveSessionMatrix(w, r)
	case "/api/cwds":
		h.serveCWDOptions(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			remainder := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
			switch {
			case remainder != "" && !strings.Contains(remainder, "/"):
				h.serveSessionHeader(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/stream"):
				h.serveSessionStream(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/todo"):
				h.serveTodoDiagnostics(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/diagnostics"):
				h.serveSessionDiagnostics(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/tools"):
				h.serveToolOutcomes(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/skills"):
				h.serveSessionSkills(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/tokens"):
				h.serveTokenSequence(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/goal"):
				h.serveGoalDiagnostics(w, r)
				return
			case strings.HasSuffix(r.URL.Path, "/detail"):
				h.serveEntryDetail(w, r)
				return
			}
		}
		writeError(w, http.StatusNotFound, "route not found")
	}
}

const unauthorizedPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Pi Session Analyzer — unauthorized</title></head>
<body><h1>Unauthorized</h1>
<p>Open the dashboard with the URL printed by <code>pi-session-analyzer dashboard</code>.
It includes a one-time <code>token</code> query parameter that is exchanged for a private
session cookie and then cleared from the address bar.</p>
<p>Rotate the token with <code>pi-session-analyzer token rotate</code> and restart the dashboard.</p>
</body></html>`

// WithAuth wraps the dashboard behind the persisted bearer token, mirroring
// mcp-broker's flow: a valid ?token= query is exchanged for an HttpOnly
// strict-same-site cookie and redirected away so the secret leaves the URL,
// Authorization: Bearer and the cookie authenticate everything else, API
// requests fail closed with 401, and page loads land on /unauthorized.
func WithAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivateHeaders(w.Header())
		if r.URL.Path == "/unauthorized" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(unauthorizedPage))
			return
		}
		if query := r.URL.Query().Get("token"); query != "" && auth.Equal(query, token) {
			auth.SetCookie(w, token)
			auth.RedirectWithoutToken(w, r)
			return
		}
		if auth.CheckBearer(r, token) || auth.CheckCookie(r, token) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		http.Redirect(w, r, "/unauthorized", http.StatusFound)
	})
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
