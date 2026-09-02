package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	modernList = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	modernPing = `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
)

func newModernBoundary(t *testing.T) (*Handler, *httpboundary.Boundary, *testAuthority) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	handler := New(Options{Authenticator: authority})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			return handler.Authenticate(ctx, request, authority)
		},
		Next: handler,
	})
	require.NoError(t, err)
	return handler, boundary, authority
}

func modernRequest(method, body string) *http.Request {
	request := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	request.Host = contract.DefaultAuthority
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("Accept", contract.MediaTypeJSON+", "+contract.MediaTypeEventStream)
	return request
}

func TestModernToolsListIsInterceptedWithoutCapabilities(t *testing.T) {
	t.Parallel()
	handler, boundary, authority := newModernBoundary(t)
	for range 2 {
		request := modernRequest(http.MethodPost, modernList)
		request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Empty(t, response.Header().Get("Mcp-Session-Id"))

		var envelope struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		assert.Equal(t, -32601, envelope.Error.Code)
		assert.NotContains(t, response.Body.String(), "listChanged")
	}
	for _, lease := range authority.captured() {
		assert.True(t, leaseDone(lease))
	}
	work, streams, legacy := handler.Status()
	assert.Equal(t, int64(0), work.InUse)
	assert.Equal(t, int64(0), streams.InUse)
	assert.Equal(t, int64(0), legacy.InUse)
}

func TestModernLeaseInvalidationCancelsInFlightRequest(t *testing.T) {
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	lease, err := authority.Authenticate(t.Context(), contract.AgentBearerPrefix+"valid")
	require.NoError(t, err)
	handler := New(Options{Authenticator: &queuedAuthenticator{leases: []*authorization.Lease{lease}}})
	started := make(chan struct{})
	cancelled := make(chan struct{})
	handler.modern = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(cancelled)
		writer.WriteHeader(http.StatusNoContent)
	})
	boundary := newLegacyBoundary(t, handler)
	request := modernRequest(http.MethodPost, modernPing)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(response, request)
		close(done)
	}()
	<-started
	lease.Release()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("lease invalidation did not cancel modern request")
	}
	<-done
	assert.True(t, leaseDone(lease))
}

func TestModernClassificationNeverDowngradesMalformedClaims(t *testing.T) {
	t.Parallel()
	_, boundary, authority := newModernBoundary(t)
	metaModern := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	initializeWithoutMirror := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
	for _, test := range []struct {
		name, method, body, header, session string
		status                              int
		allow                               string
	}{
		{name: "missing meta", method: http.MethodPost, body: initializeWithoutMirror, header: contract.ModernProtocolVersion, status: 400},
		{name: "missing header", method: http.MethodPost, body: metaModern, status: 400},
		{name: "mismatched mirror", method: http.MethodPost, body: strings.ReplaceAll(metaModern, contract.ModernProtocolVersion, contract.LegacyProtocolVersion), header: contract.ModernProtocolVersion, status: 400},
		{name: "modern init cannot fall through", method: http.MethodPost, body: initializeWithoutMirror, status: 400},
		{name: "modern get rejected", method: http.MethodGet, header: contract.ModernProtocolVersion, status: 405, allow: "POST"},
		{name: "modern delete rejected", method: http.MethodDelete, header: contract.ModernProtocolVersion, status: 405, allow: "POST"},
		{name: "modern session state rejected", method: http.MethodPost, body: modernList, header: contract.ModernProtocolVersion, session: "legacy", status: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := modernRequest(test.method, test.body)
			if test.header != "" {
				request.Header.Set("Mcp-Protocol-Version", test.header)
			}
			if test.session != "" {
				request.Header.Set("Mcp-Session-Id", test.session)
			}
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			assert.Equal(t, test.allow, response.Header().Get("Allow"))
		})
	}
	for _, lease := range authority.captured() {
		assert.True(t, leaseDone(lease))
	}
}
