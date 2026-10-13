package servers

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const privateHTTPTransport = `{"kind":"streamable_http","url":"http://notes-mcp:8080/mcp","protocol_mode":"auto","authentication":{"mode":"none"}`

func TestPrivateHTTPConfigurationStrictOptIn(t *testing.T) {
	for _, value := range []string{"", `,"allow_private_http":false`, `,"allow_private_http":null`, `,"allow_private_http":"true"`, `,"allow_private_http":1`, `,"allow_private_http":{}`, `,"allow_private_http":true,"allow_private_http":true`} {
		_, err := DecodeTransport([]byte(privateHTTPTransport + value + "}"))
		require.Error(t, err, value)
	}
	transport, err := DecodeTransport([]byte(privateHTTPTransport + `,"allow_private_http":true}`))
	require.NoError(t, err)
	require.True(t, transport.(contract.StreamableHTTPTransport).AllowPrivateHTTP)
	for _, url := range []string{"http://8.8.8.8/mcp", "http://169.254.169.254/mcp", "http://100.64.0.1/mcp", "http://[::]/mcp", "http://[ff02::1]/mcp", "http://224.0.0.1/mcp", "http://notes-mcp:80/mcp", "http://Notes-MCP/mcp"} {
		changed := transport.(contract.StreamableHTTPTransport)
		changed.URL = url
		assert.Error(t, validateTransport(changed), url)
	}
	for _, auth := range []contract.HTTPAuthentication{contract.BearerAuthentication{Mode: contract.AuthenticationBearer}, contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth}} {
		changed := transport.(contract.StreamableHTTPTransport)
		changed.Authentication = auth
		assert.Error(t, validateTransport(changed))
	}
	for _, url := range []string{"http://127.0.0.1/mcp", "http://[::1]/mcp", "https://example.com/mcp"} {
		changed := transport.(contract.StreamableHTTPTransport)
		changed.URL, changed.AllowPrivateHTTP = url, false
		assert.NoError(t, validateTransport(changed), url)
	}
}

func TestPrivateHTTPPersistsAndRevocationIsBehavioral(t *testing.T) {
	entropy := new(sequenceReader)
	repository, store, ownership := newRepository(t, entropy)
	transport, err := DecodeTransport([]byte(privateHTTPTransport + `,"allow_private_http":true}`))
	require.NoError(t, err)
	created, err := repository.Create(t.Context(), CreateRequest{Definition: Definition{Namespace: "private", DisplayName: "Private", Enabled: true, Transport: transport}, Idempotency: idempotency("private", "private", "")})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	reopened, err := storage.Open(t.Context(), ownership)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	repository, err = New(reopened, &mutableClock{now: testTime}, entropy)
	require.NoError(t, err)
	stored, err := repository.Get(t.Context(), created.Server.ID)
	require.NoError(t, err)
	assert.Equal(t, created.Server.Transport, stored.Transport)
	decoded, err := DecodeTransport(stored.Transport)
	require.NoError(t, err)
	assert.True(t, decoded.(contract.StreamableHTTPTransport).AllowPrivateHTTP)
	name := "Renamed"
	preserved, err := repository.Patch(t.Context(), stored.ID, stored.DesiredRevision, Patch{DisplayName: &name})
	require.NoError(t, err)
	assert.Equal(t, stored.Transport, preserved.Transport)
	assert.Nil(t, preserved.Operation)
	revoked := decoded.(contract.StreamableHTTPTransport)
	revoked.AllowPrivateHTTP = false
	_, err = repository.Patch(t.Context(), stored.ID, preserved.DesiredRevision, Patch{Transport: revoked})
	require.Error(t, err, "cannot retain private HTTP endpoint after removing permission")
	// A valid replacement revokes permission and advances the ordinary behavioral fence.
	revoked.URL = "https://example.com/mcp"
	patched, err := repository.Patch(t.Context(), stored.ID, preserved.DesiredRevision, Patch{Transport: revoked})
	require.NoError(t, err)
	require.NotNil(t, patched.Operation)
	assert.Equal(t, contract.OperationActivate, patched.Operation.Kind)
	assert.NotEqual(t, preserved.DesiredRevision, patched.DesiredRevision)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(patched.Transport, &fields))
	assert.NotContains(t, fields, "allow_private_http")
}
