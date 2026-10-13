package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateHTTPAPIRequiresExplicitBooleanAndReturnsPolicy(t *testing.T) {
	service := new(fakeServerService)
	handler := newServerTestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "private"}
	const transport = `{"kind":"streamable_http","url":"http://notes-mcp:8080/mcp","protocol_mode":"auto","authentication":{"mode":"none"},"allow_private_http":true}`
	created := perform(handler, http.MethodPost, "/api/v2/mcp/servers", `{"namespace":"private","display_name":"Private","enabled":false,"transport":`+transport+`}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.True(t, service.create.Definition.Transport.(contract.StreamableHTTPTransport).AllowPrivateHTTP)
	assert.Contains(t, created.Body.String(), `"allow_private_http":true`)
	got := perform(handler, http.MethodGet, "/api/v2/mcp/servers/"+testID, "", headers)
	require.Equal(t, http.StatusOK, got.Code)
	assert.Contains(t, got.Body.String(), `"allow_private_http":true`)
	delete(headers, "Idempotency-Key")
	headers["If-Match"] = contract.ServerETag(testID, "1")
	for _, value := range []string{`false`, `null`, `"true"`, `1`, `{}`} {
		response := perform(handler, http.MethodPatch, "/api/v2/mcp/servers/"+testID, `{"transport":{"kind":"streamable_http","url":"http://notes-mcp:8080/mcp","protocol_mode":"auto","authentication":{"mode":"none"},"allow_private_http":`+value+`}}`, headers)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
	patched := perform(handler, http.MethodPatch, "/api/v2/mcp/servers/"+testID, `{"transport":`+transport+`}`, headers)
	require.Equal(t, http.StatusOK, patched.Code, patched.Body.String())
	assert.True(t, service.patch.Transport.(contract.StreamableHTTPTransport).AllowPrivateHTTP)
}
