package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestID_AssignedImmediately verifies that every request handled by the
// proxy is assigned a non-empty ULID, visible via the audit record in context.
func TestRequestID_AssignedImmediately(t *testing.T) {
	auth := newTestAuthority(t)

	var capturedCtx context.Context
	inj := &stubInjector{
		headerToSet: map[string]string{},
		scope:       "",
	}
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		capturedCtx = r.Context()
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
		Rules:                stubEngineReturning(allowMatchResultWithInject()),
		Injector:             inj,
	})

	r := httptest.NewRequest(http.MethodGet, "https://example.com:443/hello", nil)
	w := httptest.NewRecorder()
	p.HandleForTest(w, r, "example.com:443", "")

	require.NotNil(t, capturedCtx, "round tripper must have been called")

	a := proxy.AuditFromContext(capturedCtx)
	require.NotNil(t, a, "audit record must be present in context")
	assert.NotEmpty(t, a.RequestID, "request ID must be assigned to every request")
}

// TestRequestID_OnDenyResponse verifies that synthesised 403 responses carry
// the X-Request-ID header and that it is a non-empty ULID string.
func TestRequestID_OnDenyResponse(t *testing.T) {
	auth := newTestAuthority(t)

	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
		Rules:                stubEngineReturning(denyMatchResult()),
	})

	resp := sendRequestThroughProxy(t, p, http.MethodGet, "example.com:443", "/secret")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	rid := resp.Header.Get("X-Request-ID")
	assert.NotEmpty(t, rid, "403 deny response must carry X-Request-ID")
	// ULID is 26 characters of Crockford Base32
	assert.Len(t, rid, 26, "X-Request-ID must be a 26-character ULID")
}

// TestRequestID_OnTimeout504 verifies that synthesised 504 responses (approval
// timeout and nil-broker case) carry the X-Request-ID header.
func TestRequestID_OnTimeout504(t *testing.T) {
	auth := newTestAuthority(t)

	// Nil broker → immediate 504 without needing a context timeout.
	p := proxy.New(proxy.Deps{
		CA:                   auth,
		UpstreamRoundTripper: roundTripperFunc(testEchoHandler),
		Rules:                stubEngineReturning(requireApprovalMatchResult()),
		// Approval deliberately nil to trigger the no-broker 504.
	})

	resp := sendRequestThroughProxy(t, p, http.MethodPost, "api.example.com:443", "/sensitive")
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)

	rid := resp.Header.Get("X-Request-ID")
	assert.NotEmpty(t, rid, "504 timeout response must carry X-Request-ID")
	assert.Len(t, rid, 26, "X-Request-ID must be a 26-character ULID")
}

// TestRequestID_NotOnForwardedResponse verifies that when the proxy forwards a
// request to the upstream, the response it proxies back does NOT have an
// X-Request-ID header injected by the proxy itself.
func TestRequestID_NotOnForwardedResponse(t *testing.T) {
	auth := newTestAuthority(t)

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		// Upstream returns a plain 200 with no X-Request-ID header.
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
		Rules:                stubEngineReturning(nil), // nil → allow, forward
	})

	resp := sendRequestThroughProxy(t, p, http.MethodGet, "example.com:443", "/hello")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rid := resp.Header.Get("X-Request-ID")
	assert.Empty(t, rid, "forwarded responses must NOT have X-Request-ID added by proxy")
}
