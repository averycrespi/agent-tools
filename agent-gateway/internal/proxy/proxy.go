// Package proxy implements the HTTP/HTTPS MITM proxy for agent-gateway.
// Connections arrive as plain HTTP CONNECT requests; the proxy terminates TLS
// on behalf of the agent using a dynamically-issued leaf certificate from the
// local CA, then forwards decoded requests to the configured upstream
// RoundTripper.
package proxy

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
)

const (
	defaultHandshakeTimeout  = 10 * time.Second
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 90 * time.Second
	defaultMaxBodyBuffer     = 1 << 20 // 1 MiB
	defaultBodyBufferTimeout = 5 * time.Second
)

// CA is the interface required by Proxy for TLS interception.
// It is satisfied by *ca.Authority but kept as an interface so tests can
// substitute a lightweight fake without importing the ca package.
//
// See also the internal/ca Authority interface definition in §13 of the design.
type CA interface {
	// ServerConfig returns a *tls.Config with a leaf certificate for host,
	// signed by the root CA. Multiple calls for the same host return the
	// same config pointer (cached).
	ServerConfig(host string) (*tls.Config, error)
}

// RulesEngine is the interface required by Proxy for evaluating per-request
// rules. It is satisfied by *rules.Engine but kept as an interface so tests
// can inject a lightweight stub.
//
// If nil, all requests are treated as if the rule engine returned a nil match
// (implicit allow).
type RulesEngine interface {
	// Evaluate returns the first matching rule result for req, or nil if no
	// rule matches. A nil result means the request is implicitly allowed.
	Evaluate(req *rules.Request) *rules.MatchResult
	// HostsForAgent returns the set of host-glob patterns with at least one
	// rule applying to agent. Used at CONNECT time to decide MITM vs tunnel.
	HostsForAgent(agent string) map[string]struct{}
	// NeedsBodyBuffer reports whether any rule that could match agent+host has
	// a body matcher, so the proxy knows whether to buffer the request body.
	NeedsBodyBuffer(agent, host string) bool
}

// ApprovalDecision is the outcome of an approval request.
type ApprovalDecision string

const (
	// DecisionApproved means the approver accepted the request; forward upstream.
	DecisionApproved ApprovalDecision = "approved"
	// DecisionDenied means the approver rejected the request; return 403.
	DecisionDenied ApprovalDecision = "denied"
	// DecisionTimeout means no decision arrived before the deadline; return 504.
	DecisionTimeout ApprovalDecision = "timeout"
)

// ApprovalRequest carries the details of an intercepted request that requires
// human (or automated) approval before being forwarded.
type ApprovalRequest struct {
	// RequestID is the ULID assigned to this request.
	RequestID string
	// Agent is the name of the authenticated agent (empty only in the test-only
	// no-registry path).
	Agent string
	// Host is the CONNECT target host:port.
	Host string
	// Method is the HTTP method.
	Method string
	// Path is the request path.
	Path string
	// Header contains the canonical request headers.
	Header http.Header
}

// ApprovalBroker is the interface used by Proxy to gate require-approval
// verdicts. The real implementation lives in the approval package; tests
// inject a stub.
//
// If nil and a require-approval verdict fires, the proxy returns 504 with the
// message "no approval broker configured".
type ApprovalBroker interface {
	// Request blocks until an approval decision is made for pending, or until
	// ctx is cancelled. Cancellation must return DecisionTimeout, nil.
	Request(ctx context.Context, pending ApprovalRequest) (ApprovalDecision, error)
}

// Injector is the interface used by Proxy to apply header mutations defined in
// a rule's inject block to an outgoing request. It is satisfied by
// *inject.Injector but kept as an interface for testability.
//
// If nil, no header injection is performed even if a matched rule has an
// inject block.
type Injector interface {
	// Apply expands the inject block of rule for agent and mutates req in place.
	// It returns the injection status, the credential scope (may be empty), and
	// any error. On inject.ErrSecretUnresolved the caller must forward the
	// request unchanged (fail-soft).
	Apply(req *http.Request, rule *rules.Rule, agent string) (inject.InjectionStatus, string, error)
}

// Deps groups all injectable dependencies for a Proxy.
type Deps struct {
	// CA provides leaf TLS configs for MITM interception. Required.
	CA CA

	// Registry authenticates agent tokens from Proxy-Authorization headers.
	// Production callers must provide one; the serve entry point refuses to
	// start if registry initialisation fails. If nil, CONNECT requests skip
	// authentication and the intercept decision — this path exists only so
	// unit tests can exercise the pipeline without stubbing authentication.
	Registry agents.Registry

	// NoInterceptHosts is the list of host-glob patterns from the
	// no_intercept_hosts config field. CONNECT targets that match any of these
	// patterns are tunnelled rather than MITM'd, regardless of the rule set.
	NoInterceptHosts []string

	// UpstreamRoundTripper is used to forward decoded requests to the real
	// upstream server. If nil, a default http.Transport is used (set during
	// Serve). Inject a fake in tests.
	UpstreamRoundTripper http.RoundTripper

	// Rules is the rules engine used to evaluate per-request verdicts.
	// If nil, all requests are forwarded (implicit allow).
	Rules RulesEngine

	// Approval is the broker used to gate require-approval verdicts.
	// If nil and a require-approval verdict fires, the proxy returns 504.
	Approval ApprovalBroker

	// Injector applies header mutations from a matched rule's inject block.
	// If nil, injection is skipped (even if the rule has an inject block).
	Injector Injector

	// Auditor records per-request audit entries to the requests table.
	// If nil, auditing is skipped (nil-safe).
	Auditor audit.Logger

	// OnRequest, if non-nil, is called with the completed audit.Entry after
	// each request is audited. It is called from the request goroutine after
	// the audit record is written (or skipped when Auditor is nil). The
	// callback must be non-blocking.
	OnRequest func(entry audit.Entry)

	// Logger is the structured logger. If nil, the default slog logger is used.
	Logger *slog.Logger

	// HandshakeTimeout is the maximum time allowed for a TLS handshake with a
	// MITM'd client. Zero uses the default of 10s.
	HandshakeTimeout time.Duration

	// ReadHeaderTimeout is the maximum time the embedded http.Server waits to
	// read HTTP/1 request headers (guards against Slowloris). Zero uses the
	// default of 10s.
	ReadHeaderTimeout time.Duration

	// IdleTimeout is the maximum time an idle HTTP/2 server connection may
	// remain open. Zero uses the default of 90s.
	IdleTimeout time.Duration

	// MaxBodyBuffer is the maximum number of bytes to buffer from the request
	// body for body-matcher evaluation. Zero uses the default of 1 MiB.
	MaxBodyBuffer int64

	// BodyBufferTimeout is the maximum wall-clock time allowed to read the
	// buffered body prefix. Zero uses the default of 5s.
	BodyBufferTimeout time.Duration
}

// Proxy is the MITM proxy. Create one with New and call Serve to accept
// connections.
type Proxy struct {
	ca                CA
	registry          agents.Registry
	noInterceptHosts  []string
	rt                http.RoundTripper
	rules             RulesEngine
	approval          ApprovalBroker
	injector          Injector
	auditor           audit.Logger
	onRequest         func(entry audit.Entry)
	log               *slog.Logger
	handshakeTimeout  time.Duration
	readHeaderTimeout time.Duration
	idleTimeout       time.Duration
	maxBodyBuffer     int64
	bodyBufferTimeout time.Duration
}

// New constructs a Proxy from the given Deps. It panics if Deps.CA is nil.
func New(d Deps) *Proxy {
	if d.CA == nil {
		panic("proxy: Deps.CA must not be nil")
	}
	rt := d.UpstreamRoundTripper
	if rt == nil {
		rt = &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				// Use the system trust store — we are strict TLS clients to
				// upstream. Verification failures become 502.
				InsecureSkipVerify: false, //nolint:gosec
			},
		}
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	ht := d.HandshakeTimeout
	if ht == 0 {
		ht = defaultHandshakeTimeout
	}
	rht := d.ReadHeaderTimeout
	if rht == 0 {
		rht = defaultReadHeaderTimeout
	}
	it := d.IdleTimeout
	if it == 0 {
		it = defaultIdleTimeout
	}
	mbb := d.MaxBodyBuffer
	if mbb == 0 {
		mbb = defaultMaxBodyBuffer
	}
	bbt := d.BodyBufferTimeout
	if bbt == 0 {
		bbt = defaultBodyBufferTimeout
	}
	return &Proxy{
		ca:                d.CA,
		registry:          d.Registry,
		noInterceptHosts:  d.NoInterceptHosts,
		rt:                rt,
		rules:             d.Rules,
		approval:          d.Approval,
		injector:          d.Injector,
		auditor:           d.Auditor,
		onRequest:         d.OnRequest,
		log:               log,
		handshakeTimeout:  ht,
		readHeaderTimeout: rht,
		idleTimeout:       it,
		maxBodyBuffer:     mbb,
		bodyBufferTimeout: bbt,
	}
}

// Serve accepts connections on ln and dispatches each to a goroutine. It
// returns when ln.Close() is called (or ln.Accept returns a permanent error).
func (p *Proxy) Serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// net.Listener.Close() causes Accept to return a non-nil error.
			// Any accept error terminates the loop.
			return
		}
		go p.serveConn(conn)
	}
}
