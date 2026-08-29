//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIM9Canary(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	dir := t.TempDir()
	createPath := filepath.Join(dir, "server.json")
	credentialPath := filepath.Join(dir, "credential.json")
	updatePath := filepath.Join(dir, "update.json")
	require.NoError(t, os.WriteFile(createPath, []byte(`{"namespace":"m9-canary","display_name":"M9 canary","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}}}`), 0o600))
	require.NoError(t, os.WriteFile(credentialPath, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"primary":"m9-secret-canary"}}`), 0o600))
	require.NoError(t, os.WriteFile(updatePath, []byte(`{"display_name":"M9 updated"}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 10)

	created := runOnlineCLI(t, harness, bearerPath, true, "server", "create", "--file", createPath, "--idempotency-key", "m9-create", "--output", "json")
	results = append(results, created)
	var creation struct {
		Server struct {
			ID              string `json:"id"`
			DesiredRevision string `json:"desired_revision"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(created.Stdout, &creation))
	serverID := creation.Server.ID
	etag := `"server-` + serverID + `-` + creation.Server.DesiredRevision + `"`

	replaced := runOnlineCLI(t, harness, bearerPath, true, "server", "credential", "replace", serverID, "--etag", etag, "--file", credentialPath, "--yes", "--output", "json")
	results = append(results, replaced)
	var replacement contract.CredentialReplacementResult
	require.NoError(t, json.Unmarshal(replaced.Stdout, &replacement))
	harness.WaitOperation(serverID, replacement.Operation.ID, contract.OperationSucceeded)

	operation := runOnlineCLI(t, harness, bearerPath, true, "server", "operation", "get", serverID, replacement.Operation.ID)
	authFlows := runOnlineCLI(t, harness, bearerPath, true, "server", "auth-flow", "list", serverID, "--limit", "1", "--output", "json")
	descriptors := runOnlineCLI(t, harness, bearerPath, true, "server", "descriptor", "list", serverID, "--retired", "include", "--output", "json")
	catalog := runOnlineCLI(t, harness, bearerPath, true, "catalog", "list", "--limit", "1", "--output", "json")
	results = append(results, operation, authFlows, descriptors, catalog)
	assert.Contains(t, string(operation.Stdout), string(contract.OperationCredentialReplace))
	assert.Contains(t, string(authFlows.Stdout), `"items":[]`)
	assert.Contains(t, string(descriptors.Stdout), `"items":[]`)

	updated := runOnlineCLI(t, harness, bearerPath, true, "server", "update", serverID, "--etag", etag, "--file", updatePath, "--output", "json")
	results = append(results, updated)
	var update struct {
		Server struct {
			DesiredRevision string `json:"desired_revision"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(updated.Stdout, &update))
	etag = `"server-` + serverID + `-` + update.Server.DesiredRevision + `"`
	listed := runOnlineCLI(t, harness, bearerPath, true, "server", "list", "--limit", "10")
	got := runOnlineCLI(t, harness, bearerPath, true, "server", "get", serverID, "--output", "json")
	results = append(results, listed, got)
	assert.Contains(t, string(listed.Stdout), "M9 updated")
	assert.Contains(t, string(got.Stdout), `"display_name":"M9 updated"`)

	deleted := runOnlineCLI(t, harness, bearerPath, true, "server", "delete", serverID, "--etag", etag, "--yes", "--output", "json")
	results = append(results, deleted)
	var deletion struct {
		Operation *contract.ServerOperation `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(deleted.Stdout, &deletion))
	require.NotNil(t, deletion.Operation)
	harness.WaitOperation(serverID, deletion.Operation.ID, contract.OperationSucceeded)

	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), "m9-secret-canary")
		assert.NotContains(t, string(result.Stderr), "m9-secret-canary")
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "M9 gate must own one Gateway lifecycle")
}
