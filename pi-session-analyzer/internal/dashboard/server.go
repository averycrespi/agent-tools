// Package dashboard serves the analyzer's private loopback-only visual interface.
package dashboard

import (
	"embed"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
)

//go:embed assets/*
var assets embed.FS

type Handler struct {
	boundary *robound.Conn
}

func NewHandler(boundary *robound.Conn) *Handler {
	return &Handler{boundary: boundary}
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
	default:
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(robound.MarshalCapped(map[string]any{"error": message})))
}
