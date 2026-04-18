package proxy

import (
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// hopByHopHeaders is the set of headers that must not be forwarded to the
// upstream (standard proxy hygiene per RFC 7230 §6.1).
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// stripHopByHop removes hop-by-hop headers from h. Per RFC 7230 §6.1 this
// includes both the fixed standard list above and any headers named in the
// Connection header value (comma-separated tokens).
func stripHopByHop(h http.Header) {
	// First, delete headers named in the Connection value (before we delete
	// Connection itself so we can still read it).
	for _, token := range strings.Split(h.Get("Connection"), ",") {
		if name := strings.TrimSpace(token); name != "" {
			h.Del(name)
		}
	}
	// Then delete the fixed standard list (including Connection itself).
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

// newRequestID returns a new ULID string using a cryptographically-random
// entropy source. Panics only if the system's random source is broken, which
// is never expected in normal operation.
func newRequestID() string {
	entropy := ulid.Monotonic(rand.Reader, 0)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// handle is the per-request handler. It forwards the request upstream via
// p.rt and copies the response back to w. Both H1 and H2 paths call this.
//
// host is the CONNECT target host:port (used to rewrite req.URL.Host and req.Host).
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request, host string) {
	// Assign a request-scoped ULID. Synthesised error responses carry this ID
	// in X-Request-ID; forwarded responses do not.
	reqID := newRequestID()

	// Evaluate rules if an engine is configured.
	if p.rules != nil {
		// hostOnly strips any port suffix for rule matching; host matchers in
		// HCL rules use bare hostnames (e.g. "api.github.com").
		hostOnly, _, err := net.SplitHostPort(host)
		if err != nil {
			hostOnly = host
		}

		rreq := &rules.Request{
			Agent:  "", // TODO(Task 24): agent name from ProxyAuth
			Host:   hostOnly,
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header,
			// Body: nil — Task 17 buffer wired in next step
		}
		m := p.rules.Evaluate(rreq)

		if m != nil && m.Rule != nil && m.Error == "" {
			switch m.Rule.Verdict {
			case "deny":
				w.Header().Set("X-Request-ID", reqID)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return

			case "require-approval":
				if p.approval == nil {
					p.log.Error("proxy: require-approval verdict but no ApprovalBroker configured",
						"request_id", reqID, "host", host)
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "no approval broker configured", http.StatusGatewayTimeout)
					return
				}
				decision, apErr := p.approval.Request(r.Context(), ApprovalRequest{
					RequestID: reqID,
					Agent:     "", // TODO(Task 24)
					Host:      host,
					Method:    r.Method,
					Path:      r.URL.Path,
					Header:    r.Header.Clone(),
				})
				if apErr != nil {
					p.log.Error("proxy: approval broker error", "request_id", reqID, "err", apErr)
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "approval error", http.StatusBadGateway)
					return
				}
				switch decision {
				case DecisionDenied:
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				case DecisionTimeout:
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "approval timed out", http.StatusGatewayTimeout)
					return
				case DecisionApproved:
					// Fall through to upstream forwarding.
				}

			case "allow":
				// Explicit allow — fall through to forwarding.

			default:
				// Unknown verdict: treat as allow to be forward-compatible.
				p.log.Warn("proxy: unknown rule verdict; treating as allow",
					"verdict", m.Rule.Verdict, "request_id", reqID)
			}
		}
		// nil match, bypass error, or allow → fall through to forwarding.
	}

	// TODO(Task 24): credential injection — apply matching rule's secret
	// binding to the outgoing request headers.

	// Build the upstream request, preserving the original context so
	// cancellation propagates (required pattern from design §13).
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, "", r.Body)
	if err != nil {
		http.Error(w, "proxy: build upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Rewrite URL to point at the real upstream.
	upReq.URL = r.URL.ResolveReference(r.URL)
	upReq.URL.Scheme = "https"
	upReq.URL.Host = host
	upReq.Host = host
	upReq.RequestURI = "" // Must be empty when using RoundTripper directly.

	// Copy headers, then strip hop-by-hop.
	upReq.Header = r.Header.Clone()
	stripHopByHop(upReq.Header)

	// Forward to upstream.
	resp, err := p.rt.RoundTrip(upReq)
	if err != nil {
		p.log.Error("proxy: upstream RoundTrip failed", "host", host, "err", err)
		http.Error(w, "proxy: upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers back to the client (strip hop-by-hop first).
	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream the response body. Flush after each Write so SSE / streaming
	// responses are forwarded promptly. The extra syscall is negligible and
	// matches standard Go proxy conventions.
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		nr, readErr := resp.Body.Read(buf)
		if nr > 0 {
			if _, writeErr := w.Write(buf[:nr]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				p.log.Debug("proxy: body read error", "err", readErr)
			}
			return
		}
	}
}
