//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayHarnessPublishesDeterministicStaticAuthorityThroughProductionAPI(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	events := harness.OpenEvents()
	require.Equal(t, http.StatusOK, events.StatusCode)
	assert.Equal(t, contract.MediaTypeEventStream, events.Header.Get("Content-Type"))
	require.NoError(t, events.Body.Close())
	var created struct {
		Server struct {
			ID                  string                       `json:"id"`
			DesiredRevision     string                       `json:"desired_revision"`
			CredentialRevisions contract.CredentialRevisions `json:"credential_revisions"`
		} `json:"server"`
	}
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers", `{"namespace":"authority-fixture","display_name":"Authority fixture","enabled":false,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"token"}}}`, map[string]string{"Idempotency-Key": "authority-fixture"}, &created)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	require.NotEmpty(t, etag)
	assert.Equal(t, "0", created.Server.CredentialRevisions.StaticCredential)

	canary := "e2e-static-authority-canary"
	var replacement contract.CredentialReplacementResult
	response = harness.AdminJSON(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/credential-replacements", `{"kind":"static_credential","expected_revision":"0","values":{"token":"`+canary+`"}}`, map[string]string{"If-Match": etag}, &replacement)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	replacementBody, err := json.Marshal(replacement)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", replacement.CredentialRevision)
	assert.False(t, bytes.Contains(replacementBody, []byte(canary)))
	harness.WaitOperation(created.Server.ID, replacement.Operation.ID, contract.OperationSucceeded)

	var current struct {
		CredentialRevisions contract.CredentialRevisions `json:"credential_revisions"`
	}
	response = harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+created.Server.ID, "", nil, &current)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "1", current.CredentialRevisions.StaticCredential)
}
