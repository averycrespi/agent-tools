package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modernList = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`

func newModernBoundary(t *testing.T) (*Handler, *httpboundary.Boundary) {
	t.Helper()
	handler := New(Options{Authenticator: &fakeAuthenticator{binding: Binding{
		PrincipalID: "01J00000000000000000000000", CredentialID: "01J00000000000000000000001", ExpiresAt: time.Now().Add(time.Hour),
	}}})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			return handler.Authenticate(ctx, request, authority)
		},
		Next: handler,
	})
	require.NoError(t, err)
	return handler, boundary
}

func modernRequest(method, body string) *http.Request {
	request := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	request.Host = contract.DefaultAuthority
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	request.Header.Set("Content-Type", contract.MediaTypeJSON)
	request.Header.Set("Accept", contract.MediaTypeJSON+", "+contract.MediaTypeEventStream)
	return request
}

func TestModernRequestUsesStatelessOfficialSDKWithoutCapabilities(t *testing.T) {
	t.Parallel()
	handler, boundary := newModernBoundary(t)
	for range 2 {
		request := modernRequest(http.MethodPost, modernList)
		request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.Empty(t, response.Header().Get("Mcp-Session-Id"))

		var envelope struct {
			Result struct {
				Tools []any `json:"tools"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
		assert.Empty(t, envelope.Result.Tools)
		assert.NotContains(t, response.Body.String(), "listChanged")
	}
	work, streams, legacy := handler.Status()
	assert.Equal(t, int64(0), work.InUse)
	assert.Equal(t, int64(0), streams.InUse)
	assert.Equal(t, int64(0), legacy.InUse)
}

func TestModernClassificationNeverDowngradesMalformedClaims(t *testing.T) {
	t.Parallel()
	_, boundary := newModernBoundary(t)
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
}
