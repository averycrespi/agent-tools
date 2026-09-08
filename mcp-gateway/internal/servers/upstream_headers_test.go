package servers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const headerTransport = `{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"auto","authentication":{"mode":"none"}`
const githubToolsets = "default,actions,gists,issues,labels,pull_requests,users"

func TestUpstreamHeaderConfigurationCompatibilityAndRejection(t *testing.T) {
	for _, extra := range []string{"", `,"headers":null`, `,"headers":{}`, `,"headers":{"X-MCP-Toolsets":"` + githubToolsets + `","X-Empty":""}`} {
		transport, err := DecodeTransport([]byte(headerTransport + extra + "}"))
		require.NoError(t, err)
		encoded, err := json.Marshal(transport)
		require.NoError(t, err)
		roundTrip, err := DecodeTransport(encoded)
		require.NoError(t, err)
		assert.Equal(t, len(transport.(contract.StreamableHTTPTransport).Headers), len(roundTrip.(contract.StreamableHTTPTransport).Headers))
		if strings.Contains(extra, "Toolsets") {
			assert.Equal(t, githubToolsets, roundTrip.(contract.StreamableHTTPTransport).Headers["X-MCP-Toolsets"])
		} else {
			assert.JSONEq(t, headerTransport+"}", string(encoded))
		}
	}
	for _, value := range []string{`[]`, `true`, `"text"`, `{"X-Test":null}`, `{"X-Test":42}`, `{"X-Test":"one","X-Test":"two"}`, `{"X-Test":"one","x-test":"two"}`, `{"Authorization":"secret"}`, `{"Mcp-Param-Region":"us"}`, `{"X-Test":"a\r\nb"}`} {
		_, err := DecodeTransport([]byte(headerTransport + `,"headers":` + value + "}"))
		require.Error(t, err, value)
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestUpstreamHeadersPersistAcrossRestartAndConditionalReplacement(t *testing.T) {
	entropy := new(sequenceReader)
	repository, store, ownership := newRepository(t, entropy)
	transport, err := DecodeTransport([]byte(headerTransport + `,"headers":{"X-MCP-Toolsets":"` + githubToolsets + `"}}`))
	require.NoError(t, err)
	created, err := repository.Create(t.Context(), CreateRequest{Definition: Definition{Namespace: "headers", DisplayName: "Headers", Enabled: true, Transport: transport}, Idempotency: idempotency("headers", "headers", "")})
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
	name := "Renamed"
	preserved, err := repository.Patch(t.Context(), stored.ID, stored.DesiredRevision, Patch{DisplayName: &name})
	require.NoError(t, err)
	assert.Equal(t, stored.Transport, preserved.Transport)
	assert.Nil(t, preserved.Operation)
	_, err = repository.Patch(t.Context(), stored.ID, stored.DesiredRevision, Patch{Transport: transport})
	assert.ErrorIs(t, err, ErrStaleRevision)
	current := preserved.Server
	for _, extra := range []string{"", `,"headers":null`, `,"headers":{}`} {
		cleared, decodeErr := DecodeTransport([]byte(headerTransport + extra + "}"))
		require.NoError(t, decodeErr)
		patched, patchErr := repository.Patch(t.Context(), current.ID, current.DesiredRevision, Patch{Transport: cleared})
		require.NoError(t, patchErr)
		require.NotNil(t, patched.Operation)
		assert.Equal(t, contract.OperationActivate, patched.Operation.Kind)
		assert.NotContains(t, string(patched.Transport), "headers")
		restored, restoreErr := repository.Patch(t.Context(), current.ID, patched.DesiredRevision, Patch{Transport: transport})
		require.NoError(t, restoreErr)
		assert.Contains(t, string(restored.Transport), githubToolsets)
		current = restored.Server
	}
}
