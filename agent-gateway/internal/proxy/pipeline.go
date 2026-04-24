package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

// Reason codes for X-Agent-Gateway-Reason. Stable strings documented in
// docs/security-model.md.
const (
	ReasonBodyMatcherBypassed = "body-matcher-bypassed"
	ReasonRuleDeny            = "rule-deny"
	ReasonUnknownVerdict      = "unknown-verdict"
	ReasonApprovalDenied      = "approval-denied"
	ReasonApprovalTimeout     = "approval-timeout"
	ReasonQueueFull           = "queue-full"
	ReasonNoApprovalBroker    = "no-approval-broker"
	ReasonSecretUnresolved    = "secret-unresolved"
	ReasonForbiddenHost       = "forbidden-host"
	ReasonBodyReadError       = "body-read-error"
)

// httpErrorWithReason writes an HTTP error with an X-Agent-Gateway-Reason
// header. Header must be set before http.Error writes headers.
func httpErrorWithReason(w http.ResponseWriter, body string, code int, reason string) {
	w.Header().Set("X-Agent-Gateway-Reason", reason)
	http.Error(w, body, code)
}

// auditCtxKey is the unexported context key for the per-request audit struct.
type auditCtxKey struct{}

// auditRecord accumulates per-request audit fields threaded through context.
// It is allocated once per request and mutated in place; callers must not copy
// the struct after placing it on a context.
type auditRecord struct {
	RequestID       string
	MatchedRule     string
	Verdict         string
	Approval        string // "approved", "denied", "timed-out"
	Injection       string // "applied", "failed"
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

// firstSecretRef scans the ReplaceHeaders values in an inject block and
// returns the name of the first ${secrets.<ident>} reference found (in
// sorted key order). Returns "" when there are no secret references.
func firstSecretRef(inj *rules.Inject) string {
	if inj == nil {
		return ""
	}
	// Iterate in sorted order for determinism.
	keys := make([]string, 0, len(inj.ReplaceHeaders))
	for k := range inj.ReplaceHeaders {
		keys = append(keys, k)
	}
	// Use strings.Clone-free sort.
	sortedKeys(keys)
	for _, k := range keys {
		m := secretRefRE.FindStringSubmatch(inj.ReplaceHeaders[k])
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

// handle is the per-request handler. It forwards the request upstream via
// p.rt and copies the response back to w. Both H1 and H2 paths call this.
//
// host is the CONNECT target host:port (used to rewrite req.URL.Host and req.Host).
// agentName is the authenticated agent name from the CONNECT handshake (empty
// only in the test-only no-registry path).
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request, host, agentName string) {
	// 1. Assign a request-scoped ULID. Synthesised error responses carry this ID
	// in X-Request-ID; forwarded responses do not.
	reqID := NewULID()
	start := time.Now()

	// 2. Initialise the per-request audit record and thread both the audit and
	// the request ID through context. The typed requestIDKey allows any package
	// that imports proxy to call RequestIDFromContext without depending on the
	// unexported auditRecord type.
	a := &auditRecord{RequestID: reqID}
	ctx := withRequestID(r.Context(), reqID)
	ctx = contextWithAudit(ctx, a)
	r = r.WithContext(ctx)

	// Audit tracking: mutable variables captured by the defer closure.
	// outcome defaults to "blocked"; set to "forwarded" only when we actually
	// stream a response back to the client.
	outcome := "blocked"
	var respStatus *int
	var bytesIn int64
	var bytesOut int64

	// hostOnly is the bare hostname used in audit entries and rule matching.
	// host has already been canonicalised at CONNECT ingress; re-normalise
	// idempotently so this layer cannot drift from the rest of the pipeline.
	// Normalize is idempotent so re-normalisation of an already-canonical
	// host cannot fail; on the improbable error we keep the split host so
	// audit rows still get a value rather than dropping the request.
	hostOnly, _, splitErr := net.SplitHostPort(host)
	if splitErr != nil {
		hostOnly = host
	}
	if canon, err := normalizeHost(hostOnly, p.log); err == nil {
		hostOnly = canon
	}

	// 2b. Defer the audit Record call so it fires regardless of which return
	// path is taken. The closure captures outcome, respStatus, and bytesIn by
	// reference so they reflect final state.
	defer func() {
		if p.auditor == nil && p.onRequest == nil {
			return
		}
		method := r.Method
		path := r.URL.Path
		entry := audit.Entry{
			ID:           reqID,
			TS:           start,
			Agent:        agentNamePtr(agentName),
			Interception: "mitm",
			Method:       &method,
			Host:         hostOnly,
			Path:         &path,
			Status:       respStatus,
			DurationMS:   time.Since(start).Milliseconds(),
			BytesIn:      bytesIn,
			BytesOut:     bytesOut,
			Outcome:      outcome,
		}
		if a.MatchedRule != "" {
			entry.MatchedRule = &a.MatchedRule
		}
		if a.Verdict != "" {
			entry.RuleVerdict = &a.Verdict
		}
		// Approval is populated later via the auditRecord if needed.
		if a.Approval != "" {
			entry.Approval = &a.Approval
		}
		switch a.Injection {
		case "applied":
			entry.Injection = &a.Injection
			if a.CredentialRef != "" {
				entry.CredentialRef = &a.CredentialRef
			}
			if a.CredentialScope != "" {
				entry.CredentialScope = &a.CredentialScope
			}
		case "failed":
			entry.Injection = &a.Injection
			if a.Error != "" {
				entry.Error = &a.Error
			}
		}
		if p.auditor != nil {
			if err := p.auditor.Record(r.Context(), entry); err != nil {
				p.log.Warn("proxy: audit record failed", "request_id", reqID, "err", err)
			}
		}
		if p.onRequest != nil {
			p.onRequest(entry)
		}
	}()

	// 3. Track the matched rule for the injection step.
	var matchedRule *rules.Rule

	// 4. Evaluate rules if an engine is configured.
	if p.rules != nil {
		rreq := &rules.Request{
			Agent:  agentName,
			Host:   hostOnly,
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header,
		}

		// Buffer the request body only when at least one rule for this
		// agent+host declares a body matcher. This avoids the buffering
		// overhead for the common case where no body matching is needed.
		needsBody := p.rules.NeedsBodyBuffer(agentName, hostOnly)
		if needsBody && r.Body != nil {
			body, truncated, timedOut, rewound, bufErr := bufferBody(
				r.Context(), r.Body, r.Header,
				p.maxBodyBuffer, p.bodyBufferTimeout,
			)
			if bufErr == nil {
				rreq.Body = body
				rreq.BodyTruncated = truncated
				rreq.BodyTimedOut = timedOut
				// Replace r.Body with the rewound reader so the upstream still
				// receives the full original body bytes.
				r.Body = rewound
			} else {
				// Fail-closed on hard I/O error when a matcher needs the body.
				// This aligns with the body_matcher_bypassed:timeout path above
				// (handled after Evaluate): if a rule author asked us to inspect
				// the body and we cannot, forwarding would silently bypass the
				// matcher the operator explicitly configured. A broken agent or
				// racy upstream tearing the body mid-read would otherwise be
				// indistinguishable from a request whose body simply did not
				// trip the matcher — collapsing the two is unsafe because the
				// operator cannot tell deny-should-have-fired from allow-did-fire.
				// The 403 carries X-Request-ID so the operator can correlate
				// the audit row (error=body_buffer_io_error) with the client's
				// failure response.
				p.log.Warn("proxy: body buffer I/O error with body matcher configured; refusing to forward",
					"request_id", reqID, "host", hostOnly, "err", bufErr)
				a.Error = "body_buffer_io_error"
				w.Header().Set("X-Request-ID", reqID)
				http.Error(w, "Forbidden: body buffer read error", http.StatusForbidden)
				return
			}
		}

		m := p.rules.Evaluate(rreq)

		// Body-matcher bypass (size cap or read timeout): the rule's body
		// condition could not be evaluated. Fail closed regardless of the
		// rule's verdict — an unevaluable rule is unsafe to forward past,
		// because we cannot know whether a deny would have fired or whether
		// an allow's narrowing condition is satisfied. The audit row records
		// the bypassed rule and the bypass reason so operators can tune
		// max_body_buffer or rewrite the rule.
		if m != nil && m.Rule != nil && m.Error != "" {
			a.MatchedRule = m.Rule.Name
			a.Verdict = m.Rule.Verdict
			a.Error = m.Error
			w.Header().Set("X-Request-ID", reqID)
			http.Error(w, "Forbidden: rule body matcher bypassed", http.StatusForbidden)
			return
		}

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
					Agent:     agentName,
					Host:      host,
					Method:    r.Method,
					Path:      r.URL.Path,
					Header:    assertedHeaders(r.Header, m.Rule.Match.Headers),
				})
				if apErr != nil {
					p.log.Error("proxy: approval broker error", "request_id", reqID, "err", apErr)
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "approval error", http.StatusBadGateway)
					return
				}
				switch decision {
				case DecisionDenied:
					a.Approval = "denied"
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				case DecisionTimeout:
					a.Approval = "timed-out"
					w.Header().Set("X-Request-ID", reqID)
					http.Error(w, "approval timed out", http.StatusGatewayTimeout)
					return
				case DecisionApproved:
					a.Approval = "approved"
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
		// nil match or allow → fall through to injection step. The bypass-
		// error case is handled above and short-circuits with 403.
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

	// Wrap the upstream request body in a counting reader so we can record
	// BytesOut (bytes sent to upstream) in the audit entry.
	if upReq.Body != nil {
		upReq.Body = &countingReadCloser{ReadCloser: upReq.Body, n: &bytesOut}
	}

	// 6. Injection step: if the matched rule has an inject block and an
	// injector is configured, apply header mutations to the upstream clone.
	// On most failures the upstream clone is discarded and the original
	// request (with its headers intact) is forwarded instead (fail-soft).
	// Host-scope violations are a policy error and MUST NOT forward — we
	// synthesise a 403 instead so the misconfig is loud.
	if matchedRule != nil && matchedRule.Inject != nil && p.injector != nil {
		a.CredentialRef = firstSecretRef(matchedRule.Inject)

		status, scope, injErr := p.injector.Apply(upReq, matchedRule, agentName, hostOnly)
		switch {
		case injErr == nil && status == inject.StatusApplied:
			a.Injection = "applied"
			a.CredentialScope = scope

		case injErr != nil && errors.Is(injErr, inject.ErrSecretInvalid):
			// Fail-closed 403: the secret decrypted but contains a byte that
			// would be unsafe to place in an HTTP header (CR/LF/NUL/C0/DEL).
			// Forwarding the original request is tempting — the agent still
			// has its dummy credential — but silence hides the misconfigured
			// secret from the operator. The 403 surfaces "secret_invalid"
			// in audit and response so the operator knows to rotate the
			// malformed secret rather than chase a generic upstream 401.
			// This is also the last defensive layer before credentials hit
			// the wire; downgrading to fail-soft would erase that guarantee.
			p.log.Warn("proxy: secret value invalid; refusing to forward",
				"request_id", reqID, "host", hostOnly, "err", injErr)
			a.Injection = "failed"
			a.Error = "secret_invalid"
			w.Header().Set("X-Request-ID", reqID)
			http.Error(w, "Forbidden: secret contains invalid characters", http.StatusForbidden)
			return

		case injErr != nil && errors.Is(injErr, inject.ErrSecretHostScopeViolation):
			// Fail-closed 403 is load-bearing: the rule matched, but the
			// secret it references is scoped to a different host. Scope is
			// THE boundary enforcing "secrets only leave the gateway to
			// their named hosts" — downgrading a violation to a soft-fail
			// would make that boundary advisory rather than enforced.
			// Silently forwarding would also let a misauthored rule (wrong
			// host glob, typo in secret scope) quietly route traffic under
			// an unintended credential pairing; the operator would see only
			// a generic upstream 401 with no indication of the mismatch.
			// The 403 names the specific problem (secret_host_scope_violation)
			// in audit and response so the operator can diagnose and fix.
			p.log.Warn("proxy: secret host scope violation; refusing to forward",
				"request_id", reqID, "host", hostOnly, "err", injErr)
			a.Injection = "failed"
			a.Error = "secret_host_scope_violation"
			w.Header().Set("X-Request-ID", reqID)
			http.Error(w, "Forbidden: secret not bound to this host", http.StatusForbidden)
			return

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
		}
	}

	// 7. Forward to upstream.
	resp, err := p.rt.RoundTrip(upReq)
	if err != nil {
		p.log.Error("proxy: upstream RoundTrip failed", "host", host, "err", err)
		http.Error(w, "proxy: upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture status for audit.
	sc := resp.StatusCode
	respStatus = &sc

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
			bytesIn += int64(nr)
			if _, writeErr := w.Write(buf[:nr]); writeErr != nil {
				// Client disconnected mid-stream: mark as forwarded since we did
				// establish the upstream response.
				outcome = "forwarded"
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
			break
		}
	}
	outcome = "forwarded"
}

// countingReadCloser wraps an io.ReadCloser and counts bytes consumed.
// It is used to measure BytesOut (bytes sent to upstream in the request body).
type countingReadCloser struct {
	io.ReadCloser
	n *int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	*c.n += int64(n)
	return n, err
}

// assertedHeaders returns a new http.Header containing only the header names
// that appear in assertedNames (the rule's Match.Headers keys). This enforces
// the §8 approval view invariant: the approval view must not contain body
// content or header values that the rule did not assert.
//
// assertedNames is the map[string]string from rules.Match.Headers; only the
// keys are used (the canonical header name, case-insensitive via http.Header).
func assertedHeaders(src http.Header, assertedNames map[string]string) http.Header {
	out := make(http.Header, len(assertedNames))
	for name := range assertedNames {
		if vals := src[http.CanonicalHeaderKey(name)]; len(vals) > 0 {
			out[http.CanonicalHeaderKey(name)] = append([]string(nil), vals...)
		}
	}
	return out
}
