package proxy_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/inject"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubInjector implements proxy.Injector for tests.
type stubInjector struct {
	// headerToSet is the header name→value to inject on success.
	headerToSet map[string]string
	// scope is the credential scope returned on success.
	scope string
	// err is the error to return (nil = success).
	err error
	// lastReq captures the upstream clone passed to Apply.
	lastReq *http.Request
}

func (s *stubInjector) Apply(req *http.Request, _ *rules.Rule, _ string) (inject.InjectionStatus, string, error) {
	s.lastReq = req
	if s.err != nil {
		return inject.StatusFailed, "", s.err
	}
	for k, v := range s.headerToSet {
		req.Header.Set(k, v)
	}
	return inject.StatusApplied, s.scope, nil
}

// LastReqContext returns the context of the most recent request seen by Apply.
// It is used in tests to read audit data threaded through context.
func (s *stubInjector) LastReqContext() context.Context {
	if s.lastReq == nil {
		return context.Background()
	}
	return s.lastReq.Context()
}

// stubRulesEngine implements proxy.RulesEngine for tests.
type stubRulesEngine struct {
	result *rules.MatchResult
}

func (s *stubRulesEngine) Evaluate(_ *rules.Request) *rules.MatchResult {
	return s.result
}

func (s *stubRulesEngine) HostsForAgent(_ string) map[string]struct{} {
	return nil
}

// stubApprovalBroker implements proxy.ApprovalBroker using a channel to
// simulate an asynchronous approval decision.
type stubApprovalBroker struct {
	decision proxy.ApprovalDecision
	delay    time.Duration
	// doneCh, if set, is closed after Request returns so callers can detect
	// that Request was actually invoked.
	doneCh chan struct{}
}

func (s *stubApprovalBroker) Request(ctx context.Context, _ proxy.ApprovalRequest) (proxy.ApprovalDecision, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			if s.doneCh != nil {
				close(s.doneCh)
			}
			return proxy.DecisionTimeout, nil
		}
	}
	if s.doneCh != nil {
		close(s.doneCh)
	}
	return s.decision, nil
}

// stubEngineReturning returns a *stubRulesEngine that always returns m.
func stubEngineReturning(m *rules.MatchResult) *stubRulesEngine {
	return &stubRulesEngine{result: m}
}

// denyMatchResult returns a MatchResult for a deny rule.
func denyMatchResult() *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{Verdict: "deny"},
	}
}

// allowMatchResult returns a MatchResult for an allow rule.
func allowMatchResult() *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{Verdict: "allow"},
	}
}

// requireApprovalMatchResult returns a MatchResult for a require-approval rule.
func requireApprovalMatchResult() *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{Verdict: "require-approval"},
	}
}

// allowMatchResultWithInject returns a MatchResult for an allow rule with an
// inject block that references a secret template.
func allowMatchResultWithInject() *rules.MatchResult {
	return &rules.MatchResult{
		Rule: &rules.Rule{
			Verdict: "allow",
			Inject: &rules.Inject{
				SetHeaders: map[string]string{
					"Authorization": "Bearer ${secrets.gh_token}",
				},
			},
		},
	}
}

// sendRequestThroughProxy exercises the proxy pipeline directly using
// httptest.ResponseRecorder so we do not need a real network connection.
// It calls handle via the HTTP handler that serveH1/serveH2 both use.
func sendRequestThroughProxy(t *testing.T, p *proxy.Proxy, method, host, path string) *http.Response {
	t.Helper()

	// Build a fake inbound request — this simulates a request that the MITM'd
	// server would receive after the TLS tunnel is up.
	r := httptest.NewRequest(method, "https://"+host+path, nil)
	w := httptest.NewRecorder()

	// Call the exported HandleForTest helper.
	p.HandleForTest(w, r, host)

	return w.Result()
}

// TestPipeline_AllowRuleForwards verifies that a nil match result (no rule
// matches) forwards the request upstream without synthesising a response.
func TestPipeline_AllowRuleForwards(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(nil), // nil match → allow
	})

	resp := sendRequestThroughProxy(t, p, http.MethodGet, "example.com:443", "/hello")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, called, "upstream RoundTripper should have been called")
}

// TestPipeline_ExplicitAllowForwards verifies that an explicit "allow" verdict
// forwards the request upstream.
func TestPipeline_ExplicitAllowForwards(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(allowMatchResult()),
	})

	resp := sendRequestThroughProxy(t, p, http.MethodGet, "example.com:443", "/hello")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, called, "upstream RoundTripper should have been called")
}

// TestPipeline_DenyRuleReturns403 verifies that a "deny" verdict returns 403
// without forwarding to the upstream.
func TestPipeline_DenyRuleReturns403(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(denyMatchResult()),
	})

	resp := sendRequestThroughProxy(t, p, http.MethodGet, "example.com:443", "/secret")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, called, "upstream should NOT have been called on deny")
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"), "deny response must have X-Request-ID")
}

// TestPipeline_RequireApproval_Approved verifies that when the approval broker
// returns "approved" the request is forwarded upstream (200).
func TestPipeline_RequireApproval_Approved(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	broker := &stubApprovalBroker{decision: proxy.DecisionApproved}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(requireApprovalMatchResult()),
		Approval:             broker,
	})

	resp := sendRequestThroughProxy(t, p, http.MethodPost, "api.example.com:443", "/sensitive")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, called, "upstream should be called after approval")
}

// TestPipeline_RequireApproval_Denied verifies that when the approval broker
// returns "denied" a 403 is synthesised.
func TestPipeline_RequireApproval_Denied(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})

	broker := &stubApprovalBroker{decision: proxy.DecisionDenied}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(requireApprovalMatchResult()),
		Approval:             broker,
	})

	resp := sendRequestThroughProxy(t, p, http.MethodPost, "api.example.com:443", "/sensitive")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, called, "upstream should NOT be called on denied")
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"), "denied response must have X-Request-ID")
}

// TestPipeline_RequireApproval_Timeout verifies that when the context times out
// before the broker responds a 504 is returned.
func TestPipeline_RequireApproval_Timeout(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})

	// Broker that deliberately blocks until context is cancelled.
	broker := &stubApprovalBroker{
		decision: proxy.DecisionTimeout,
		delay:    10 * time.Second, // will be interrupted by context
	}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(requireApprovalMatchResult()),
		Approval:             broker,
	})

	// Use a very short timeout so the test is fast.
	r := httptest.NewRequest(http.MethodPost, "https://api.example.com:443/sensitive", nil)
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
	defer cancel()
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()

	p.HandleForTest(w, r, "api.example.com:443")

	resp := w.Result()
	assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	assert.False(t, called, "upstream should NOT be called on timeout")
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"), "timeout response must have X-Request-ID")
}

// TestPipeline_InjectsOnAllow verifies that when the injector succeeds the
// upstream receives the injected header and the audit context records
// injection="applied".
func TestPipeline_InjectsOnAllow(t *testing.T) {
	auth := newTestAuthority(t)

	var upstreamAuthHeader string
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		upstreamAuthHeader = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	inj := &stubInjector{
		headerToSet: map[string]string{"Authorization": "Bearer injected-token"},
		scope:       "agent:test",
	}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(allowMatchResultWithInject()),
		Injector:             inj,
	})

	// Send a request with a dummy credential that should be replaced.
	r := httptest.NewRequest(http.MethodGet, "https://api.example.com:443/repos", nil)
	r.Header.Set("Authorization", "Bearer dummy-cred")
	w := httptest.NewRecorder()
	p.HandleForTest(w, r, "api.example.com:443")

	resp := w.Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The upstream received the injected header.
	assert.Equal(t, "Bearer injected-token", upstreamAuthHeader,
		"upstream must receive the injected Authorization header")

	// The original inbound request is unchanged (injector worked on the clone).
	assert.Equal(t, "Bearer dummy-cred", r.Header.Get("Authorization"),
		"original request must not be mutated")

	// Audit context must record injection="applied".
	a := proxy.AuditFromContext(inj.LastReqContext())
	require.NotNil(t, a, "audit must be present in request context")
	assert.Equal(t, "applied", a.Injection, "audit.injection must be 'applied'")
	assert.Equal(t, "agent:test", a.CredentialScope)
}

// TestPipeline_FailSoftOnUnresolvedSecret verifies that when the injector
// returns ErrSecretUnresolved the request is forwarded unchanged (dummy
// credential intact) and the audit context records injection="failed",
// error="secret_unresolved".
func TestPipeline_FailSoftOnUnresolvedSecret(t *testing.T) {
	auth := newTestAuthority(t)

	var upstreamAuthHeader string
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		upstreamAuthHeader = r.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})

	inj := &stubInjector{
		err: fmt.Errorf("inject set_header %q: %w", "Authorization", inject.ErrSecretUnresolved), //nolint:goerr113
	}

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(allowMatchResultWithInject()),
		Injector:             inj,
	})

	// Send a request with a dummy credential that must be preserved.
	r := httptest.NewRequest(http.MethodGet, "https://api.example.com:443/repos", nil)
	r.Header.Set("Authorization", "Bearer dummy-cred")
	w := httptest.NewRecorder()
	p.HandleForTest(w, r, "api.example.com:443")

	resp := w.Result()
	// Fail-soft: request is forwarded (200 from upstream stub).
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"fail-soft: request must still be forwarded upstream")

	// Upstream receives the original dummy credential unchanged.
	assert.Equal(t, "Bearer dummy-cred", upstreamAuthHeader,
		"upstream must receive the original (unmodified) Authorization header on fail-soft")

	// Audit context records the failure.
	a := proxy.AuditFromContext(inj.LastReqContext())
	require.NotNil(t, a, "audit must be present in request context")
	assert.Equal(t, "failed", a.Injection, "audit.injection must be 'failed'")
	assert.Equal(t, "secret_unresolved", a.Error, "audit.error must be 'secret_unresolved'")
}

// TestPipeline_RequireApproval_NilBroker verifies that when no approval broker
// is configured a 504 is returned with a useful message.
func TestPipeline_RequireApproval_NilBroker(t *testing.T) {
	auth := newTestAuthority(t)

	var called bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: rt,
		Rules:                stubEngineReturning(requireApprovalMatchResult()),
		// Approval intentionally nil
	})

	resp := sendRequestThroughProxy(t, p, http.MethodPost, "api.example.com:443", "/sensitive")
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
	assert.False(t, called, "upstream should NOT be called when no broker")
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"), "nil-broker timeout response must have X-Request-ID")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "no approval broker", "body should explain the 504 reason")
}
