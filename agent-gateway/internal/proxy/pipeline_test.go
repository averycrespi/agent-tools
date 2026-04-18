package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
