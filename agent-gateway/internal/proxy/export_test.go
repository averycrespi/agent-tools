package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"golang.org/x/net/http2"
)

// NewH2ServerForTest returns the *http2.Server that the Proxy uses for the
// MITM'd HTTP/2 path. Tests use this to assert that hardening limits
// (MaxConcurrentStreams etc.) are configured.
func (p *Proxy) NewH2ServerForTest() *http2.Server {
	return p.newH2Server()
}

// NewH1ServerForTest returns the *http.Server that the Proxy uses for the
// MITM'd HTTP/1 path. Tests use this to assert that hardening limits
// (MaxHeaderBytes, timeouts) are configured.
func (p *Proxy) NewH1ServerForTest() *http.Server {
	return p.newH1Server(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
}

// HandleForTest exposes the internal handle method for white-box testing of
// the pipeline verdict dispatch without requiring a full TLS connection.
// agentName is the authenticated agent name (empty string for unauthenticated tests).
func (p *Proxy) HandleForTest(w http.ResponseWriter, r *http.Request, host, agentName string) {
	p.handle(w, r, host, agentName)
}

// ParseAuthForTest exposes parseAuth for white-box testing.
func ParseAuthForTest(header string) (token string, ok bool) {
	return parseAuth(header)
}

// AuthenticateForTest exposes Authenticate for white-box testing.
func AuthenticateForTest(ctx context.Context, registry agents.Registry, hdr http.Header) (*agents.Agent, error) {
	return Authenticate(ctx, registry, hdr)
}

// DecideForTest exposes Decide for white-box testing.
func DecideForTest(ctx context.Context, host string, ag *agents.Agent, engine RulesEngine, noIntercept []string) ConnectDecision {
	return Decide(ctx, host, ag, engine, noIntercept)
}

// AuditRecord is the exported mirror of auditRecord for use in tests.
// Fields are a 1:1 copy so tests can inspect audit state without importing
// unexported types.
type AuditRecord struct {
	RequestID       string
	MatchedRule     string
	Verdict         string
	Approval        string
	Injection       string
	CredentialScope string
	CredentialRef   string
	Error           string
}

// AuditFromContext retrieves the audit record stored in ctx and returns it as
// an *AuditRecord. Returns nil when no audit is present.
func AuditFromContext(ctx context.Context) *AuditRecord {
	a := auditFromContext(ctx)
	if a == nil {
		return nil
	}
	return &AuditRecord{
		RequestID:       a.RequestID,
		MatchedRule:     a.MatchedRule,
		Verdict:         a.Verdict,
		Approval:        a.Approval,
		Injection:       a.Injection,
		CredentialScope: a.CredentialScope,
		CredentialRef:   a.CredentialRef,
		Error:           a.Error,
	}
}

// ServeTunnelForTest exposes serveTunnel for white-box testing so tests can
// invoke the tunnel path without setting up a full authenticated CONNECT flow.
func (p *Proxy) ServeTunnelForTest(conn net.Conn, br *bufio.Reader, connectTarget, agentName string) {
	p.serveTunnel(conn, br, connectTarget, agentName)
}
