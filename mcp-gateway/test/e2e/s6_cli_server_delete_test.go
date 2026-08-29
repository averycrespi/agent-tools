//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIServerDelete(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	createBody := []byte(`{"namespace":"cli-delete","display_name":"CLI delete","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`)
	created := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/servers", createBody, map[string]string{"Idempotency-Key": "cli-delete"})
	require.Equal(t, http.StatusCreated, created.StatusCode, string(created.Body))
	var createdMutation struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(created.Body, &createdMutation))
	serverID, serverETag := createdMutation.Server.ID, created.Header.Get("ETag")
	results := make([]testutil.ProcessResult, 0, 7)

	refused := runOnlineCLI(t, harness, bearerPath, false, "server", "delete", serverID, "--etag", serverETag, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	assert.Contains(t, string(refused.Stderr), `"code":"client_invalid_input"`)
	stillPresent := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+serverID, nil)
	assert.Equal(t, http.StatusOK, stillPresent.StatusCode)

	stale := runOnlineCLI(t, harness, bearerPath, false, "server", "delete", serverID, "--etag", `"server-`+serverID+`-999"`, "--yes", "--output", "json")
	results = append(results, stale)
	assert.Equal(t, 5, stale.ExitCode)
	assert.Contains(t, string(stale.Stderr), `"code":"stale_revision"`)

	deleted := runOnlineCLI(t, harness, bearerPath, true, "server", "delete", serverID, "--etag", serverETag, "--yes", "--output", "json")
	results = append(results, deleted)
	var mutation struct {
		Server struct {
			ID           string                      `json:"id"`
			DesiredState contract.DesiredServerState `json:"desired_state"`
			Transport    json.RawMessage             `json:"transport"`
		} `json:"server"`
		Operation *contract.ServerOperation `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(deleted.Stdout, &mutation))
	assert.Equal(t, contract.DesiredServerDeleted, mutation.Server.DesiredState)
	assert.JSONEq(t, "null", string(mutation.Server.Transport))
	require.NotNil(t, mutation.Operation)
	assert.Equal(t, contract.OperationDelete, mutation.Operation.Kind)
	harness.WaitOperation(serverID, mutation.Operation.ID, contract.OperationSucceeded)

	tombstone := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+serverID, nil)
	tombstoneETag := tombstone.Header.Get("ETag")
	replayed := runOnlineCLI(t, harness, bearerPath, true, "server", "delete", serverID, "--etag", tombstoneETag, "--yes")
	results = append(results, replayed)
	assert.Contains(t, string(replayed.Stdout), "deleted")
	assert.Contains(t, string(replayed.Stdout), string(contract.OperationDelete))

	force := runOnlineCLI(t, harness, bearerPath, false, "server", "delete", serverID, "--etag", tombstoneETag, "--force", "--output", "json")
	results = append(results, force)
	assert.Equal(t, 2, force.ExitCode)

	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write(deleted.Stdout)
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "server", "delete", serverID, "--etag", tombstoneETag, "--yes", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Contains(t, string(uncertain.Stderr), `"uncertain":true`)
	assert.Contains(t, string(uncertain.Stderr), "Nothing was retried")
	assert.Contains(t, string(uncertain.Stderr), "remote revocation may remain incomplete")

	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T40 must own one Gateway lifecycle")
}
