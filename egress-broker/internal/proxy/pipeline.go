package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/egress-broker/internal/auth"
	"github.com/averycrespi/agent-tools/egress-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/egress-broker/internal/hostnorm"
	"github.com/averycrespi/agent-tools/egress-broker/internal/netguard"
	"github.com/averycrespi/agent-tools/egress-broker/internal/rules"
)

// hopByHopHeaders are removed before forwarding, per RFC 7230 section 6.1.
// They describe the single hop between client and proxy, not the end-to-end
// request, so passing them through would confuse the upstream.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// handleAbsolute serves an absolute-form plain HTTP request.
//
// It enters exactly the same pipeline the intercepted path uses, so plain HTTP
// gets the same modes, the same injection, and the same audit shape (AC-14).
func (p *Proxy) handleAbsolute(w http.ResponseWriter, r *http.Request) {
	if !auth.CheckProxyAuth(r.Header.Get("Proxy-Authorization"), p.authToken()) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="egress-broker"`)
		p.refuse(w, http.StatusProxyAuthRequired, ReasonUnauthenticated, "proxy authentication required")
		return
	}

	// The request-line scheme is authoritative and must be checked before
	// anything else derives from it. An absolute-form "https://" request line
	// parses into a URL whose Port() is empty, so assuming plain HTTP here
	// would default the port to 80 and forward an intercepted, credential-
	// injected request in cleartext. Only plain HTTP travels in absolute form;
	// TLS reaches this proxy as CONNECT.
	if r.URL.Scheme != "http" {
		p.refuse(w, http.StatusBadRequest, ReasonBadRequest,
			"this proxy accepts absolute-form request lines for http only; send https through CONNECT")
		return
	}

	host, err := hostnorm.Normalize(r.URL.Hostname())
	if err != nil || host == "" {
		p.refuse(w, http.StatusBadRequest, ReasonBadRequest, "the request URL has an invalid host")
		return
	}

	port := 80
	if raw := r.URL.Port(); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			p.refuse(w, http.StatusBadRequest, ReasonBadRequest, "the request URL has an invalid port")
			return
		}
		port = parsed
	}

	// Run the CONNECT-time decision first, exactly as the HTTPS path does.
	// Without this the fallthrough policy would never apply to plain HTTP: a
	// request matching no rule would fall through to Evaluate's implicit-allow
	// and be forwarded even under fallthrough "deny" (AC-14).
	engine := p.rules.Engine()
	decision := engine.ConnectDecision(host, port)

	switch decision.Action {
	case rules.ConnectDeny:
		p.audit.Record(Event{
			ID: newRequestID(), Start: time.Now(), Interception: "http",
			Method: r.Method, Host: host, Port: port, Path: r.URL.Path,
			Query: redactQuery(r.URL.RawQuery), Status: http.StatusForbidden,
			MatchedRule: decision.Rule, Mode: decision.Mode, Outcome: OutcomeBlocked,
		})
		p.refuse(w, http.StatusForbidden, denyReason(decision.Mode), decision.Reason)
		return

	case rules.ConnectTunnel:
		// There is no TLS to avoid terminating here, so "tunnel" for plain
		// HTTP means forward untouched: no injection, no rule attribution.
		p.forwardPlain(w, r, host, port, decision)
		return

	case rules.ConnectMITM:
		p.handleIntercepted(w, r, host, port, "http")
		return
	}
}

// forwardPlain forwards a plain-HTTP request without evaluating rules or
// injecting, which is what a tunnel decision means when there is no TLS.
func (p *Proxy) forwardPlain(w http.ResponseWriter, r *http.Request, host string, port int, decision rules.ConnectResult) {
	start := time.Now()
	event := Event{
		ID: newRequestID(), Start: start, Interception: "http",
		Method: r.Method, Host: host, Port: port, Path: r.URL.Path,
		Query:       redactQuery(r.URL.RawQuery),
		MatchedRule: decision.Rule, Mode: decision.Mode,
	}

	upstreamReq, err := p.buildUpstreamRequest(r, host, port, "http", rules.Decision{}, nil)
	if err != nil {
		event.Status = http.StatusBadRequest
		event.Outcome = OutcomeError
		event.DurationMS = sinceMS(start)
		p.audit.Record(event)
		p.refuse(w, http.StatusBadRequest, ReasonBadRequest, "could not build the upstream request")
		return
	}

	resp, err := p.transport.RoundTrip(upstreamReq)
	if err != nil {
		p.recordRoundTripFailure(&event, err)
		p.refuseRoundTrip(w, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	event.BytesIn = p.streamResponse(w, resp)
	event.Status = resp.StatusCode
	event.Outcome = OutcomeAllowed
	event.BytesOut = r.ContentLength
	event.DurationMS = sinceMS(start)
	p.audit.Record(event)
}

// handleIntercepted runs one request through evaluation, injection, and the
// upstream round trip.
//
// host and port come from the CONNECT target on the MITM path and from the
// absolute-form URL on the plain-HTTP path. Either way they are authoritative
// over anything the request's own Host header says (D10).
func (p *Proxy) handleIntercepted(w http.ResponseWriter, r *http.Request, host string, port int, scheme string) {
	start := time.Now()
	id := newRequestID()

	event := Event{
		ID: id, Start: start, Interception: "mitm",
		Method: r.Method, Host: host, Port: port, Path: r.URL.Path,
		Query: redactQuery(r.URL.RawQuery),
	}
	if scheme == "http" {
		event.Interception = "http"
	}

	engine := p.rules.Engine()
	decision := engine.Evaluate(host, port, r.Method, r.URL.Path)
	event.MatchedRule = decision.Rule
	event.Mode = decision.Mode

	if decision.Action == rules.RequestDeny {
		event.Status = http.StatusForbidden
		event.Outcome = OutcomeBlocked
		event.DurationMS = sinceMS(start)
		p.audit.Record(event)
		p.refuse(w, http.StatusForbidden, ReasonRuleDeny, decision.Reason)
		return
	}

	// D17: every credential is resolved and host-checked before any header is
	// written. A failure here must leave the upstream having seen nothing.
	var injected map[string]string
	if decision.Inject != nil && len(decision.Inject.Set) > 0 {
		expanded, err := p.resolver.ExpandAll(decision.Inject.Set, host)
		if err != nil {
			p.recordInjectionFailure(&event, err)
			p.refuseInjection(w, err)
			return
		}
		injected = expanded.Headers
		event.CredentialRef = strings.Join(expanded.Names, ",")
		event.Injection = strconv.Itoa(len(expanded.Headers))
	}

	upstreamReq, err := p.buildUpstreamRequest(r, host, port, scheme, decision, injected)
	if err != nil {
		event.Status = http.StatusBadRequest
		event.Outcome = OutcomeError
		event.Error = sanitizeDetail(err.Error())
		event.DurationMS = sinceMS(start)
		p.audit.Record(event)
		p.refuse(w, http.StatusBadRequest, ReasonBadRequest, "could not build the upstream request")
		return
	}

	resp, err := p.transport.RoundTrip(upstreamReq)
	if err != nil {
		p.recordRoundTripFailure(&event, err)
		p.refuseRoundTrip(w, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	written := p.streamResponse(w, resp)

	event.Status = resp.StatusCode
	event.Outcome = OutcomeAllowed
	event.BytesIn = written
	event.BytesOut = r.ContentLength
	event.DurationMS = sinceMS(start)
	p.audit.Record(event)
}

// recordRoundTripFailure audits a failed upstream call, distinguishing a
// policy refusal from a transport error.
func (p *Proxy) recordRoundTripFailure(event *Event, err error) {
	event.Status = http.StatusBadGateway
	event.Outcome = OutcomeError
	if errors.Is(err, netguard.ErrBlocked) {
		event.Status = http.StatusForbidden
		event.Outcome = OutcomeBlocked
	}
	// Sanitised for the same reason the client-facing detail is: this is the
	// only audit path that runs after a credential has already been placed on
	// the outgoing request, so an unconstrained error string is the one place
	// credential-adjacent detail could reach a stored row.
	event.Error = sanitizeDetail(err.Error())
	event.DurationMS = sinceMS(event.Start)
	p.audit.Record(*event)
}

func (p *Proxy) refuseRoundTrip(w http.ResponseWriter, err error) {
	if errors.Is(err, netguard.ErrBlocked) {
		p.refuse(w, http.StatusForbidden, ReasonBlockedAddress, "the upstream address is blocked")
		return
	}
	p.refuse(w, http.StatusBadGateway, ReasonUpstreamFailure, "upstream request failed")
}

// recordInjectionFailure tags the audit row so an operator can tell an
// unresolvable credential from a host-scope violation (AC-8, AC-9).
func (p *Proxy) recordInjectionFailure(event *Event, err error) {
	event.Status = http.StatusForbidden
	event.Outcome = OutcomeBlocked
	event.DurationMS = sinceMS(event.Start)

	switch {
	case errors.Is(err, credentials.ErrHostScope):
		event.Error = "credential_host_scope_violation"
	case errors.Is(err, credentials.ErrNotFound), errors.Is(err, credentials.ErrUnavailable):
		event.Error = "credential_unresolved"
	case errors.Is(err, credentials.ErrInvalidValue):
		event.Error = "credential_invalid_value"
	default:
		event.Error = "credential_error"
	}
	p.audit.Record(*event)
}

func (p *Proxy) refuseInjection(w http.ResponseWriter, err error) {
	reason := ReasonCredentialUnresolved
	detail := "a credential referenced by the matching rule could not be resolved"
	switch {
	case errors.Is(err, credentials.ErrHostScope):
		reason = ReasonCredentialScope
		detail = "a credential referenced by the matching rule is not bound to this host"
	case errors.Is(err, credentials.ErrInvalidValue):
		// The credential resolved; its value simply cannot go in a header.
		// Saying "could not be resolved" would send an operator looking in the
		// wrong place.
		reason = ReasonCredentialInvalid
		detail = "a credential referenced by the matching rule holds a value that cannot be sent in a header"
	}
	// The detail never carries the underlying error, which could name the
	// credential's bound hosts, and never the value (AC-10).
	p.refuse(w, http.StatusForbidden, reason, detail)
}

// buildUpstreamRequest constructs the request sent upstream.
//
// The context is the client request's, so a client that disconnects cancels
// the upstream call rather than leaving it running.
func (p *Proxy) buildUpstreamRequest(
	r *http.Request, host string, port int, scheme string,
	decision rules.Decision, injected map[string]string,
) (*http.Request, error) {
	target := &url.URL{
		Scheme:   scheme,
		Host:     upstreamAuthority(host, port, scheme),
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, fmt.Errorf("building the upstream request: %w", err)
	}
	req.ContentLength = r.ContentLength

	// Copy the client's headers, minus hop-by-hop ones.
	copyHeaders(req.Header, r.Header)
	removeHopByHop(req.Header, r.Header.Get("Connection"))

	// Rule-directed removals happen before injection, so a rule can strip a
	// header and set its own value for the same name.
	if decision.Inject != nil {
		for _, name := range decision.Inject.Remove {
			req.Header.Del(name)
		}
	}
	for name, value := range injected {
		req.Header.Set(name, value)
	}

	// The Host header follows the CONNECT target (D10).
	req.Host = target.Host
	return req, nil
}

// upstreamAuthority renders host:port, omitting the port when it is the
// scheme's default so upstreams see the Host header they expect.
func upstreamAuthority(host string, port int, scheme string) string {
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// streamResponse copies the upstream response to the client incrementally.
//
// It flushes after every chunk. Buffering to completion would break streaming
// endpoints — server-sent events, long polls, token-by-token model output —
// which is exactly the traffic an agent generates (AC-13).
func (p *Proxy) streamResponse(w http.ResponseWriter, resp *http.Response) int64 {
	copyHeaders(w.Header(), resp.Header)
	removeHopByHop(w.Header(), resp.Header.Get("Connection"))
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	written, _ := io.Copy(&flushWriter{w: w, flusher: flusher}, resp.Body)
	return written
}

// flushWriter flushes after every write, so each chunk the upstream produced
// reaches the client as its own chunk.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// removeHopByHop strips the fixed hop-by-hop headers plus anything the
// Connection header names, per RFC 7230.
func removeHopByHop(h http.Header, connection string) {
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
	for _, name := range strings.Split(connection, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			h.Del(trimmed)
		}
	}
}
