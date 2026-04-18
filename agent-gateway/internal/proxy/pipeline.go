package proxy

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// auditCtxKey is the unexported context key for the per-request audit struct.
type auditCtxKey struct{}

// auditRecord accumulates per-request audit fields threaded through context.
// It is allocated once per request and mutated in place; callers must not copy
// the struct after placing it on a context.
type auditRecord struct {
	RequestID       string
	MatchedRule     string
	Verdict         string
	Injection       string // "applied", "failed", "none"
	CredentialScope string
	CredentialRef   string
	Error           string
}

// contextWithAudit returns a copy of ctx carrying a (already-allocated) audit.
func contextWithAudit(ctx context.Context, a *auditRecord) context.Context {
	return context.WithValue(ctx, auditCtxKey{}, a)
}

// auditFromContext retrieves the *auditRecord stored in ctx, or nil if absent.
func auditFromContext(ctx context.Context) *auditRecord {
	a, _ := ctx.Value(auditCtxKey{}).(*auditRecord)
	return a
}

// secretRefRE matches ${secrets.<ident>} tokens in inject template strings.
var secretRefRE = regexp.MustCompile(`\$\{secrets\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// firstSecretRef scans the SetHeaders values in an inject block and returns the
// name of the first ${secrets.<ident>} reference found (in sorted key order).
// Returns "" when there are no secret references.
func firstSecretRef(inj *rules.Inject) string {
	if inj == nil {
		return ""
	}
	// Iterate in sorted order for determinism.
	keys := make([]string, 0, len(inj.SetHeaders))
	for k := range inj.SetHeaders {
		keys = append(keys, k)
	}
	// Use strings.Clone-free sort.
	sortedKeys(keys)
	for _, k := range keys {
		m := secretRefRE.FindStringSubmatch(inj.SetHeaders[k])
		if m != nil {
			return m[1]
		}
	}
	return ""
}

// sortedKeys sorts a slice of strings in-place using simple insertion sort.
// For the small numbers of headers in inject blocks this is fine.
func sortedKeys(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

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
	// 1. Assign a request-scoped ULID. Synthesised error responses carry this ID
	// in X-Request-ID; forwarded responses do not.
	reqID := newRequestID()

	// 2. Initialise the per-request audit record and thread it through context.
	a := &auditRecord{RequestID: reqID}
	ctx := contextWithAudit(r.Context(), a)
	r = r.WithContext(ctx)

	// 3. Track the matched rule for the injection step.
	var matchedRule *rules.Rule

	// 4. Evaluate rules if an engine is configured.
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
			matchedRule = m.Rule
			a.MatchedRule = m.Rule.Name
			a.Verdict = m.Rule.Verdict

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
					// Fall through to injection step.
				}

			case "allow":
				// Explicit allow — fall through to injection step.

			default:
				// Unknown verdict: treat as allow to be forward-compatible.
				p.log.Warn("proxy: unknown rule verdict; treating as allow",
					"verdict", m.Rule.Verdict, "request_id", reqID)
			}
		}
		// nil match, bypass error, or allow → fall through to injection step.
	}

	// 5. Build the upstream request as a CLONE of the inbound request so that
	// the original r (from the agent) is never mutated. The context is shared
	// so cancellation propagates and the audit record is accessible from both.
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

	// 6. Injection step: if the matched rule has an inject block and an
	// injector is configured, apply header mutations to the upstream clone.
	// On failure the upstream clone is discarded and the original request
	// (with its headers intact) is forwarded instead (fail-soft).
	if matchedRule != nil && matchedRule.Inject != nil && p.injector != nil {
		a.CredentialRef = firstSecretRef(matchedRule.Inject)

		status, scope, injErr := p.injector.Apply(upReq, matchedRule, a.MatchedRule)
		switch {
		case injErr == nil && status == inject.StatusApplied:
			a.Injection = "applied"
			a.CredentialScope = scope

		case injErr != nil && errors.Is(injErr, inject.ErrSecretUnresolved):
			// Fail-soft: log, record audit, forward original request unchanged.
			p.log.Warn("proxy: injection failed (secret unresolved); forwarding original request",
				"request_id", reqID, "err", injErr)
			a.Injection = "failed"
			a.Error = "secret_unresolved"
			// Reset upReq headers to the original (unmodified) clone so the
			// agent's dummy credential passes through.
			upReq.Header = r.Header.Clone()
			stripHopByHop(upReq.Header)

		case injErr != nil:
			// Fail-soft for other injection errors too.
			p.log.Warn("proxy: injection failed; forwarding original request",
				"request_id", reqID, "err", injErr)
			a.Injection = "failed"
			a.Error = injErr.Error()
			upReq.Header = r.Header.Clone()
			stripHopByHop(upReq.Header)

		default:
			// StatusNoInject (rule has no inject block) — should not happen here
			// since we already checked matchedRule.Inject != nil, but handle it.
			a.Injection = "none"
		}
	} else {
		a.Injection = "none"
	}

	// 7. Forward to upstream.
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
