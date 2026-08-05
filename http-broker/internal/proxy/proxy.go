// Package proxy implements the forward-proxy listener: CONNECT handling,
// absolute-form plain HTTP, the tunnel relay, and the MITM pipeline.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
	"github.com/averycrespi/agent-tools/http-broker/internal/ca"
	"github.com/averycrespi/agent-tools/http-broker/internal/credentials"
	"github.com/averycrespi/agent-tools/http-broker/internal/hostnorm"
	"github.com/averycrespi/agent-tools/http-broker/internal/netguard"
	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

// ReasonHeader carries a short machine-readable explanation on every
// synthesized 4xx/5xx, so an agent that gets a refusal can tell policy from a
// network failure without reading the audit log.
const ReasonHeader = "X-Http-Broker-Reason"

// Reason values. These are stable strings; the README documents them.
const (
	ReasonUnauthenticated = "proxy-auth-required"
	ReasonRuleDeny        = "rule-deny"
	ReasonFallthroughDeny = "fallthrough-deny"
	ReasonBlockedAddress  = "blocked-address"
	ReasonBadRequest      = "bad-request"
	ReasonUpstreamFailure = "upstream-failure"

	// The two below are reason codes sent to the client, not secrets; gosec's
	// G101 heuristic flags them only because the name contains "credential".
	ReasonCredentialScope      = "credential-host-scope" //nolint:gosec // reason code, not a credential
	ReasonCredentialUnresolved = "credential-unresolved" //nolint:gosec // reason code, not a credential
	ReasonCredentialInvalid    = "credential-invalid"    //nolint:gosec // reason code, not a credential
)

// Timeouts. The header deadline bounds how long a client may hold a connection
// without stating its intent; the tunnel idle timeout bounds a relay that has
// gone quiet in both directions.
const (
	DefaultHeaderTimeout = 30 * time.Second
	DefaultTunnelIdle    = 5 * time.Minute
)

// AuditSink receives one record per request. The proxy never blocks on it and
// never fails a request because auditing failed.
type AuditSink interface {
	Record(Event)
}

// Event is one auditable request or tunnel.
type Event struct {
	ID           string
	Start        time.Time
	Interception string // "tunnel" or "mitm"
	Method       string
	Host         string
	Port         int
	Path         string
	Query        string
	Status       int
	DurationMS   int64
	BytesIn      int64
	BytesOut     int64
	MatchedRule  string
	// Source is what decided — a rule, the fallthrough policy, or an implicit
	// allow — and Mode is what this proxy did with the bytes. Two axes, so a
	// row decided by policy rather than by a rule still says what happened.
	Source        string
	Mode          string
	Injection     string
	CredentialRef string
	Outcome       string
	Error         string
}

// Outcome values recorded in the audit log.
const (
	OutcomeAllowed = "allowed"
	OutcomeBlocked = "blocked"
	OutcomeError   = "error"
)

// Options configures a Proxy.
type Options struct {
	Rules         *rules.Store
	Authority     *ca.Authority
	Resolver      *credentials.Resolver
	Dialer        *netguard.Dialer
	Audit         AuditSink
	Token         string
	Logger        *slog.Logger
	HeaderTimeout time.Duration
	TunnelIdle    time.Duration
}

// Proxy serves the forward-proxy listener.
type Proxy struct {
	rules         *rules.Store
	authority     *ca.Authority
	resolver      *credentials.Resolver
	dialer        *netguard.Dialer
	audit         AuditSink
	log           *slog.Logger
	headerTimeout time.Duration
	tunnelIdle    time.Duration

	// token is swapped on SIGHUP rather than captured at construction. A
	// leaked token is answered by `token rotate` plus a reload, and that
	// procedure is worthless if the running process keeps honouring the old
	// value until someone restarts it.
	token atomic.Pointer[string]

	// transports holds one upstream transport per TLS floor. See
	// newUpstreamTransports for why the floor cannot be per-request.
	transports map[uint16]*http.Transport

	// hijacked counts relays that http.Server.Shutdown cannot see. A CONNECT
	// is removed from the server's active-connection set the moment it is
	// hijacked, so without this the documented drain window would elapse
	// instantly and cut live tunnels.
	hijacked sync.WaitGroup
}

// WaitForRelays blocks until every hijacked relay has finished, or until ctx
// is done. The composition root calls it during shutdown.
func (p *Proxy) WaitForRelays(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.hijacked.Wait()
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// New returns a Proxy.
func New(opts Options) *Proxy {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.HeaderTimeout == 0 {
		opts.HeaderTimeout = DefaultHeaderTimeout
	}
	if opts.TunnelIdle == 0 {
		opts.TunnelIdle = DefaultTunnelIdle
	}
	if opts.Audit == nil {
		opts.Audit = discardSink{}
	}

	p := &Proxy{
		rules:         opts.Rules,
		authority:     opts.Authority,
		resolver:      opts.Resolver,
		dialer:        opts.Dialer,
		audit:         opts.Audit,
		log:           opts.Logger,
		headerTimeout: opts.HeaderTimeout,
		tunnelIdle:    opts.TunnelIdle,
	}
	p.SetToken(opts.Token)
	p.transports = p.newUpstreamTransports()
	return p
}

// SetToken replaces the token the proxy authenticates against. Called on
// SIGHUP so a rotated token takes effect without a restart.
func (p *Proxy) SetToken(token string) { p.token.Store(&token) }

// authToken returns the token in force for this request.
func (p *Proxy) authToken() string {
	if t := p.token.Load(); t != nil {
		return *t
	}
	return ""
}

type discardSink struct{}

func (discardSink) Record(Event) {}

// ServeHTTP handles both proxy request forms.
//
// A forward proxy sees two shapes: CONNECT for tunnelled or intercepted TLS,
// and an absolute-form request line ("GET http://host/path") for plain HTTP.
// Anything else is a client that has been pointed at this port by mistake.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	if r.URL != nil && r.URL.IsAbs() {
		p.handleAbsolute(w, r)
		return
	}

	p.refuse(w, http.StatusBadRequest, ReasonBadRequest,
		"this is a forward proxy: send a CONNECT or an absolute-form request, or set HTTP_PROXY and HTTPS_PROXY")
}

// handleConnect implements the CONNECT-time decision.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// Assigned here so every row this connection produces is identifiable.
	// Without it the CONNECT-path rows all share an empty primary key and
	// SQLite keeps only the first, silently losing every later refusal.
	id := newRequestID()

	if !auth.CheckProxyAuth(r.Header.Get("Proxy-Authorization"), p.authToken()) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="http-broker"`)
		p.recordEarlyRefusal(id, start, "tunnel", r.Method,
			http.StatusProxyAuthRequired, "proxy authentication failed")
		p.refuse(w, http.StatusProxyAuthRequired, ReasonUnauthenticated,
			"proxy authentication required")
		return
	}

	host, port, err := splitTarget(r.Host)
	if err != nil {
		p.recordEarlyRefusal(id, start, "tunnel", r.Method,
			http.StatusBadRequest, "malformed CONNECT target")
		p.refuse(w, http.StatusBadRequest, ReasonBadRequest, err.Error())
		return
	}

	engine := p.rules.Engine()
	decision := engine.ConnectDecision(host, port)

	switch decision.Action {
	case rules.ConnectDeny:
		p.audit.Record(Event{
			ID: id, Start: start, Interception: "tunnel", Host: host, Port: port,
			Status: http.StatusForbidden, MatchedRule: decision.Rule,
			Source: decision.Source, Mode: decision.Mode,
			Outcome: OutcomeBlocked, Error: sanitizeDetail(decision.Reason), DurationMS: sinceMS(start),
		})
		p.refuse(w, http.StatusForbidden, denyReason(decision.Source), decision.Reason)
		return

	case rules.ConnectTunnel:
		p.serveTunnel(w, r, id, host, port, decision, start)
		return

	case rules.ConnectMITM:
		p.serveMITM(w, r, id, host, port, engine, decision, start)
		return
	}
}

// denyReason maps what decided a refusal to a stable reason string.
func denyReason(source string) string {
	switch source {
	case rules.SourceFallthrough:
		return ReasonFallthroughDeny
	default:
		return ReasonRuleDeny
	}
}

// serveTunnel relays bytes without terminating TLS.
//
// The dial goes through the netguard dialer, exactly as the MITM path does.
// The implementation this was modelled on guarded only its MITM transport and
// called a bare net.Dial here, which left the tunnel as an open route to cloud
// metadata (D8).
func (p *Proxy) serveTunnel(w http.ResponseWriter, r *http.Request, id, host string, port int, decision rules.ConnectResult, start time.Time) {
	target := net.JoinHostPort(host, strconv.Itoa(port))

	ctx := netguard.WithAllowPrivate(r.Context(), decision.AllowPrivate)
	upstream, err := p.dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		p.recordDialFailure(id, start, "tunnel", host, port, decision, err)
		p.refuseDial(w, err)
		return
	}
	defer func() { _ = upstream.Close() }()

	client, err := hijack(w)
	if err != nil {
		p.log.Error("hijacking the client connection failed", "error", err)
		return
	}
	defer func() { _ = client.Close() }()

	p.hijacked.Add(1)
	defer p.hijacked.Done()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}

	sent, received := relay(client, upstream, p.tunnelIdle)

	// A tunnel reveals no method or path — that is what tunnelling means — so
	// those columns stay empty rather than being guessed at (AC-4).
	p.audit.Record(Event{
		ID: id, Start: start, Interception: "tunnel", Host: host, Port: port,
		MatchedRule: decision.Rule, Source: decision.Source, Mode: decision.Mode, Outcome: OutcomeAllowed,
		BytesOut: sent, BytesIn: received, DurationMS: sinceMS(start),
	})
}

func (p *Proxy) recordDialFailure(id string, start time.Time, interception, host string, port int, decision rules.ConnectResult, err error) {
	outcome := OutcomeError
	if errors.Is(err, netguard.ErrBlocked) {
		outcome = OutcomeBlocked
	}
	p.audit.Record(Event{
		ID: id, Start: start, Interception: interception, Host: host, Port: port,
		MatchedRule: decision.Rule, Source: decision.Source, Mode: decision.Mode, Outcome: outcome,
		// Sanitised like every other audit error path, so the invariant holds
		// uniformly rather than depending on which failure fired.
		Status: dialStatus(err), Error: sanitizeDetail(err.Error()), DurationMS: sinceMS(start),
	})
}

func dialStatus(err error) int {
	if errors.Is(err, netguard.ErrBlocked) {
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}

func (p *Proxy) refuseDial(w http.ResponseWriter, err error) {
	if errors.Is(err, netguard.ErrBlocked) {
		p.refuse(w, http.StatusForbidden, ReasonBlockedAddress, err.Error())
		return
	}
	p.refuse(w, http.StatusBadGateway, ReasonUpstreamFailure, "upstream dial failed")
}

// refuse writes a synthesized response carrying the reason header.
//
// The detail goes in the body rather than the header so a long explanation
// cannot produce an unparseable header, and so a value carrying a control byte
// could not split the header block.
//
// Some details embed the client's own CONNECT target, so the body is sanitised
// and served as nosniff text/plain: an agent rendering a proxy error in a
// browser-like context must not be able to turn it into markup.
func (p *Proxy) refuse(w http.ResponseWriter, status int, reason, detail string) {
	w.Header().Set(ReasonHeader, reason)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if detail != "" {
		//nolint:gosec // G705 flags reflected output; sanitizeDetail strips
		// everything outside printable ASCII (including angle brackets) and the
		// response is nosniff text/plain, so there is no markup to inject into.
		_, _ = fmt.Fprintln(w, sanitizeDetail(detail))
	}
}

// maxDetailBytes bounds a refusal body so an oversized client-supplied target
// cannot be reflected at length.
const maxDetailBytes = 512

// sanitizeDetail reduces a message to printable ASCII, replacing anything else
// with "?", and truncates it.
func sanitizeDetail(s string) string {
	if len(s) > maxDetailBytes {
		s = s[:maxDetailBytes]
	}
	out := []byte(s)
	for i := range out {
		c := out[i]
		if c < 0x20 || c > 0x7e {
			out[i] = '?'
		}
		if c == '<' || c == '>' {
			out[i] = '?'
		}
	}
	return string(out)
}

// splitTarget parses a CONNECT target into a normalised host and a port.
//
// Normalisation happens here, before any glob comparison, so rules never see a
// "host:port" string and a rule naming a bare host matches on any port (AC-16).
func splitTarget(target string) (string, int, error) {
	if target == "" {
		return "", 0, errors.New("CONNECT requires a host:port target")
	}

	rawHost, rawPort, err := net.SplitHostPort(target)
	if err != nil {
		// A CONNECT without a port is malformed, but be explicit about it.
		return "", 0, fmt.Errorf("CONNECT target %q must be host:port", target)
	}

	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("CONNECT target %q has an invalid port", target)
	}

	host, err := hostnorm.Normalize(rawHost)
	if err != nil {
		return "", 0, fmt.Errorf("CONNECT target %q has an invalid host", target)
	}
	if host == "" {
		return "", 0, fmt.Errorf("CONNECT target %q has an empty host", target)
	}
	return host, port, nil
}

// hijack takes over the raw connection underlying an HTTP response.
func hijack(w http.ResponseWriter) (net.Conn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("the server does not support hijacking, which CONNECT requires")
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}
	// Anything the client pipelined after its CONNECT is already buffered and
	// must be forwarded, or the first bytes of the TLS handshake are lost.
	if buf != nil && buf.Reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, buffered: buf}, nil
	}
	return conn, nil
}

func sinceMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
