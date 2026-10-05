package httpproxy

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
)

// Each handler/connection gets independent diagnostic entropy, not audit identity
// or client metadata. Missing correlation never changes forwarding authority.
func proxyID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}

type failureWriter struct {
	http.ResponseWriter
	id      string
	started bool
}

func (w *failureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *failureWriter) WriteHeader(status int) {
	if status >= 200 {
		w.started = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *failureWriter) Write(p []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(p)
}
func (w *failureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.started = true
	}
	return c, b, err
}

func upstreamStatus(err error) int {
	switch {
	case errors.Is(err, remote.ErrAddressPolicy):
		return http.StatusForbidden
	case errors.Is(err, remote.ErrProxyTimeout), transferCondition(err) == "timeout":
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

func (e *Engine) observeTransfer(w http.ResponseWriter, stage string, err error) {
	var observed *transferError
	if errors.As(err, &observed) {
		stage = observed.stage
	}
	if errors.Is(err, remote.ErrProxyDeadline) {
		stage = "deadline"
	}
	typed := diagnostics.ProxyRead
	switch stage {
	case "downstream_read":
		typed = diagnostics.ProxyDownstreamRead
	case "upstream_write":
		typed = diagnostics.ProxyUpstreamWrite
	case "connect":
		typed = diagnostics.ProxyConnect
	case "downstream_write":
		typed = diagnostics.ProxyWrite
	case "downstream_flush":
		typed = diagnostics.ProxyFlush
	case "deadline":
		typed = diagnostics.ProxyDeadline
	}
	e.observeFailure(w, typed, err)
}

func (e *Engine) observeFailure(w http.ResponseWriter, stage diagnostics.Stage, err error) {
	id := ""
	if tracked, ok := w.(*failureWriter); ok {
		id = tracked.id
	}
	cause := proxyFailureCause(err)
	e.observeProxy(time.Now(), diagnostics.HTTPProxyFailure, stage, cause, id)
}
