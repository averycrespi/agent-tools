package proxy

import (
	"io"
	"net/http"
	"strings"
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

// handle is the per-request handler. It forwards the request upstream via
// p.rt and copies the response back to w. Both H1 and H2 paths call this.
//
// host is the CONNECT target hostname (used to rewrite req.URL).
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request, host string) {
	// TODO(Task 24): rules evaluation — check per-agent rule set, apply
	// allow/deny/require-approval verdict. For this milestone every request
	// is forwarded unconditionally.

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
