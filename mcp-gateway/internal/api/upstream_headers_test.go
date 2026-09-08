package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHeadersAPIConfigurationAndIsolation(t *testing.T) {
	const value = "default,actions,gists,issues,labels,pull_requests,users"
	service := new(fakeServerService)
	handler := newServerTestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "headers", "X-MCP-Toolsets": "inbound-must-not-win"}
	const transport = `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"none"},"headers":{"X-MCP-Toolsets":"` + value + `"}}`
	created := perform(handler, http.MethodPost, "/api/v1/servers", `{"namespace":"headers","display_name":"Headers","enabled":false,"transport":`+transport+`}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Equal(t, value, service.create.Definition.Transport.(contract.StreamableHTTPTransport).Headers["X-MCP-Toolsets"])
	assert.Contains(t, created.Body.String(), value)
	assert.Empty(t, created.Header().Get("X-MCP-Toolsets"))
	got := perform(handler, http.MethodGet, "/api/v1/servers/"+testID, "", headers)
	require.Equal(t, http.StatusOK, got.Code)
	assert.Contains(t, got.Body.String(), value)
	assert.Empty(t, got.Header().Get("X-MCP-Toolsets"))
	delete(headers, "Idempotency-Key")
	headers["If-Match"] = contract.ServerETag(testID, "1")
	patched := perform(handler, http.MethodPatch, "/api/v1/servers/"+testID, `{"transport":`+transport+`}`, headers)
	require.Equal(t, http.StatusOK, patched.Code, patched.Body.String())
	assert.Equal(t, value, service.patch.Transport.(contract.StreamableHTTPTransport).Headers["X-MCP-Toolsets"])
	for _, raw := range []string{`{"Authorization":"secret-canary"}`, `{"X-Test":null}`, `{"X-Test":"one","x-test":"two"}`} {
		response := perform(handler, http.MethodPatch, "/api/v1/servers/"+testID, `{"transport":{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"none"},"headers":`+raw+`}}`, headers)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), `"field":"transport.headers"`)
		assert.NotContains(t, response.Body.String(), "secret-canary")
	}
}
