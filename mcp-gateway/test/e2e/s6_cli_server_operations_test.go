//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIServerOperations(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	server := harness.SetupCurrentCatalog("cli-ops", []fixtureTool{{Name: "safe", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	inputDir := t.TempDir()
	refreshPath := filepath.Join(inputDir, "refresh.json")
	reloadPath := filepath.Join(inputDir, "reload.json")
	disconnectPath := filepath.Join(inputDir, "disconnect.json")
	unknownPath := filepath.Join(inputDir, "unknown.json")
	require.NoError(t, os.WriteFile(refreshPath, []byte(`{"kind":"refresh_catalog"}`), 0o600))
	require.NoError(t, os.WriteFile(reloadPath, []byte(`{"kind":"reload"}`), 0o600))
	require.NoError(t, os.WriteFile(disconnectPath, []byte(`{"kind":"disconnect_credentials"}`), 0o600))
	require.NoError(t, os.WriteFile(unknownPath, []byte(`{"kind":"retry","extra":true}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 10)

	refusedReload := runOnlineCLI(t, harness, bearerPath, false, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", reloadPath, "--output", "json")
	results = append(results, refusedReload)
	assert.Equal(t, 2, refusedReload.ExitCode)
	refusedDisconnect := runOnlineCLI(t, harness, bearerPath, false, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", disconnectPath, "--output", "json")
	results = append(results, refusedDisconnect)
	assert.Equal(t, 2, refusedDisconnect.ExitCode)

	unknown := runOnlineCLI(t, harness, bearerPath, false, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", unknownPath, "--idempotency-key", "unused", "--output", "json")
	results = append(results, unknown)
	assert.Equal(t, 2, unknown.ExitCode)

	started := runOnlineCLI(t, harness, bearerPath, true, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", refreshPath, "--idempotency-key", "refresh-once", "--output", "json")
	results = append(results, started)
	var mutation contract.ServerOperationMutation
	require.NoError(t, json.Unmarshal(started.Stdout, &mutation))
	assert.Equal(t, server.ServerID, mutation.Operation.ServerID)
	assert.Equal(t, contract.OperationRefreshCatalog, mutation.Operation.Kind)
	harness.WaitOperation(server.ServerID, mutation.Operation.ID, contract.OperationSucceeded)

	replayed := runOnlineCLI(t, harness, bearerPath, true, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", refreshPath, "--idempotency-key", "refresh-once", "--output", "json")
	results = append(results, replayed)
	var replayMutation contract.ServerOperationMutation
	require.NoError(t, json.Unmarshal(replayed.Stdout, &replayMutation))
	assert.Equal(t, mutation.Operation.ID, replayMutation.Operation.ID)

	conflict := runOnlineCLI(t, harness, bearerPath, false, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", reloadPath, "--idempotency-key", "refresh-once", "--yes", "--output", "json")
	results = append(results, conflict)
	assert.Equal(t, 5, conflict.ExitCode)
	assert.Contains(t, string(conflict.Stderr), `"code":"idempotency_conflict"`)

	listed := runOnlineCLI(t, harness, bearerPath, true, "server", "operation", "list", server.ServerID, "--limit", "10", "--output", "json")
	results = append(results, listed)
	var page contract.Collection[contract.ServerOperation]
	require.NoError(t, json.Unmarshal(listed.Stdout, &page))
	assert.NotEmpty(t, page.Items)
	got := runOnlineCLI(t, harness, bearerPath, true, "server", "operation", "get", server.ServerID, mutation.Operation.ID)
	results = append(results, got)
	assert.Contains(t, string(got.Stdout), mutation.Operation.ID)
	assert.Contains(t, string(got.Stdout), string(contract.OperationRefreshCatalog))

	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"operation":{}}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "server", "operation", "start", server.ServerID, "--etag", server.ETag, "--file", refreshPath, "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Contains(t, string(uncertain.Stderr), `"uncertain":true`)
	assert.Contains(t, string(uncertain.Stderr), "Nothing was retried or polled")
	assert.Regexp(t, regexp.MustCompile(`idempotency key [A-Za-z0-9_-]{32}`), string(uncertain.Stderr))
	assert.Contains(t, string(uncertain.Stderr), strings.Trim(server.ETag, `"`))
	assert.Contains(t, string(uncertain.Stderr), "sha256:")

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
	assert.Len(t, harness.results, 1, "T41 must own one Gateway lifecycle")
}
