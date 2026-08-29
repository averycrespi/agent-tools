//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIServerCredentials(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	createBody := []byte(`{"namespace":"cli-credentials","display_name":"CLI credentials","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}}}`)
	created := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/servers", createBody, map[string]string{"Idempotency-Key": "cli-credentials"})
	require.Equal(t, http.StatusCreated, created.StatusCode, string(created.Body))
	var createdMutation struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(created.Body, &createdMutation))
	serverID, serverETag := createdMutation.Server.ID, created.Header.Get("ETag")
	inputDir := t.TempDir()
	secretCanary := "credential-cli-canary-7Yp3"
	staticPath := filepath.Join(inputDir, "static.json")
	invalidPath := filepath.Join(inputDir, "invalid.json")
	oauthPath := filepath.Join(inputDir, "oauth.json")
	require.NoError(t, os.WriteFile(staticPath, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"primary":"`+secretCanary+`"}}`), 0o600))
	require.NoError(t, os.WriteFile(invalidPath, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"primary":"secret"},"client_secret":"wrong-union"}`), 0o600))
	require.NoError(t, os.WriteFile(oauthPath, []byte(`{"kind":"oauth_client","expected_revision":"0","client_secret":"oauth-cli-canary-8Zq4"}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 7)

	refused := runOnlineCLI(t, harness, bearerPath, false, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", staticPath, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	invalid := runOnlineCLI(t, harness, bearerPath, false, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", invalidPath, "--yes", "--output", "json")
	results = append(results, invalid)
	assert.Equal(t, 2, invalid.ExitCode)

	replaced := runOnlineCLI(t, harness, bearerPath, true, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", staticPath, "--yes", "--output", "json")
	results = append(results, replaced)
	var replacement contract.CredentialReplacementResult
	require.NoError(t, json.Unmarshal(replaced.Stdout, &replacement))
	assert.Equal(t, contract.ServerCredentialStatic, replacement.Kind)
	assert.Equal(t, "1", replacement.CredentialRevision)
	assert.Equal(t, contract.OperationCredentialReplace, replacement.Operation.Kind)
	assert.NotContains(t, string(replaced.Stdout), secretCanary)
	harness.WaitOperation(serverID, replacement.Operation.ID, contract.OperationSucceeded)

	stale := runOnlineCLI(t, harness, bearerPath, false, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", staticPath, "--yes", "--output", "json")
	results = append(results, stale)
	assert.Equal(t, 5, stale.ExitCode)
	assert.Contains(t, string(stale.Stderr), `"code":"stale_revision"`)

	var oauthObserved atomic.Bool
	oauthResult := replacement
	oauthResult.Kind = contract.ServerCredentialOAuthClient
	oauthResult.CredentialRevision = "1"
	oauthBody, err := json.Marshal(oauthResult)
	require.NoError(t, err)
	fakeOAuth := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		oauthObserved.Store(request.Header.Get("If-Match") == serverETag && request.Header.Get("Idempotency-Key") == "" && string(body) == `{"kind":"oauth_client","expected_revision":"0","client_secret":"oauth-cli-canary-8Zq4"}`)
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write(oauthBody)
	}))
	defer fakeOAuth.Close()
	oauth := runCLIAt(t, harness, bearerPath, fakeOAuth.URL, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", oauthPath, "--yes", "--output", "json")
	results = append(results, oauth)
	assert.True(t, oauthObserved.Load())
	assert.NotContains(t, string(oauth.Stdout), "oauth-cli-canary-8Zq4")

	fakeUncertain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fakeUncertain.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fakeUncertain.URL, "server", "credential", "replace", serverID, "--etag", serverETag, "--file", staticPath, "--yes", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Contains(t, string(uncertain.Stderr), `"uncertain":true`)
	assert.Contains(t, string(uncertain.Stderr), "Nothing was replayed")
	assert.Contains(t, string(uncertain.Stderr), "reads cannot prove")

	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), secretCanary)
		assert.NotContains(t, string(result.Stderr), secretCanary)
		assert.NotContains(t, string(result.Stdout), "oauth-cli-canary-8Zq4")
		assert.NotContains(t, string(result.Stderr), "oauth-cli-canary-8Zq4")
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T42 must own one Gateway lifecycle")
}
