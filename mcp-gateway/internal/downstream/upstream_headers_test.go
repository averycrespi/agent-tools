package downstream

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHeadersAreImmutableServerScopedAndMCPOnly(t *testing.T) {
	const value = "default,actions,gists,issues,labels,pull_requests,users"
	requests := make(chan http.Header, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	endpoint, err := remote.ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	factory := remote.New(remote.Options{})
	for _, auth := range []string{"", "Bearer static-canary", "Bearer oauth-canary"} {
		headers := map[string]string{"X-MCP-Toolsets": value, "X-Empty": ""}
		transport, err := NewHTTPTransport(factory, endpoint, auth, headers)
		require.NoError(t, err)
		headers["X-MCP-Toolsets"] = "mutated"
		for _, method := range []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/call", "notifications/cancelled"} {
			message := Message{Payload: []byte(`{}`), Method: method}
			if strings.HasPrefix(method, "notifications/") {
				_, err = transport.Notify(t.Context(), message)
			} else {
				_, err = transport.Exchange(t.Context(), message)
			}
			require.NoError(t, err)
			request := <-requests
			assert.Equal(t, value, request.Get("X-MCP-Toolsets"), method)
			assert.Contains(t, request, "X-Empty")
			assert.Equal(t, auth, request.Get("Authorization"))
			assert.Empty(t, request.Get("Cookie"))
		}
		require.NoError(t, transport.Close(t.Context()))
	}
	other, err := NewHTTPTransport(factory, endpoint, "", nil)
	require.NoError(t, err)
	_, err = other.Exchange(t.Context(), Message{Payload: []byte(`{}`), Method: "tools/list"})
	require.NoError(t, err)
	assert.Empty(t, (<-requests).Get("X-MCP-Toolsets"))
	require.NoError(t, other.Close(t.Context()))
	// OAuth roles share the factory, not the downstream transport or its headers.
	for _, path := range []string{"/metadata", "/registration", "/token"} {
		oauthEndpoint, parseErr := remote.ParseEndpoint(server.URL+path, true)
		require.NoError(t, parseErr)
		_, err = factory.Exchange(t.Context(), remote.Request{Endpoint: oauthEndpoint, Method: http.MethodPost, Body: []byte(`{}`), MaxBody: 1024})
		require.NoError(t, err)
		assert.Empty(t, (<-requests).Get("X-MCP-Toolsets"))
	}
}

func TestUpstreamHeadersRejectOverridesAndCombinedLimitsBeforeHandoff(t *testing.T) {
	endpoint, err := remote.ParseEndpoint("http://127.0.0.1:1/mcp", true)
	require.NoError(t, err)
	factory := remote.New(remote.Options{})
	for _, headers := range []map[string]string{{"Authorization": "x"}, {"Mcp-Param-Region": "x"}, {"X-A": "1", "x-a": "2"}, {"X-A": "x\r\ny"}} {
		_, err := NewHTTPTransport(factory, endpoint, "", headers)
		assert.ErrorIs(t, err, ErrInvalidMessage)
	}
	transport, err := NewHTTPTransport(factory, endpoint, "Bearer "+strings.Repeat("a", 8185), map[string]string{"X-Test": strings.Repeat("x", 4096)})
	require.NoError(t, err)
	for _, mode := range []string{"count", "bytes", "value"} {
		params := map[string]string{}
		switch mode {
		case "count":
			for index := range 100 {
				params[fmt.Sprintf("P%d", index)] = "v"
			}
		case "bytes":
			for index := range 3 {
				params[fmt.Sprintf("P%d", index)] = strings.Repeat("v", 8192)
			}
		case "value":
			params["P"] = strings.Repeat("v", 8193)
		}
		handedOff := false
		_, err := transport.Exchange(t.Context(), Message{Method: "tools/call", Payload: []byte(`{}`), ParameterHeaders: params, MarkHandoff: func() { handedOff = true }})
		require.Error(t, err, mode)
		assert.False(t, handedOff, mode)
	}
	require.NoError(t, transport.Close(t.Context()))
}
