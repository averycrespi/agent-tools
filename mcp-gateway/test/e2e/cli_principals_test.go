//go:build e2e

package e2e

import (
	"encoding/json"
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

func runCLIPrincipalInputMatrix(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	results := make([]testutil.ProcessResult, 0, 10)

	invalid := runOnlineCLI(t, harness, bearerPath, false, "principal", "create", "--file", filepath.Join(t.TempDir(), "removed.json"), "--output", "json")
	results = append(results, invalid)
	assert.Equal(t, 2, invalid.ExitCode)
	created := runOnlineCLI(t, harness, bearerPath, true, "principal", "create", "--display-name", "CLI principal", "--visibility", "requestable", "--output", "json")
	results = append(results, created)
	var creation contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Stdout, &creation))
	principalID := creation.Principal.ID
	assert.Equal(t, principalID, creation.DefaultGrant.PrincipalID)
	assert.Equal(t, contract.GrantAllow, creation.DefaultGrant.Effect)
	etag := contract.PrincipalETag(principalID, creation.Principal.Revision)

	listed := runOnlineCLI(t, harness, bearerPath, true, "principal", "list", "--limit", "10", "--output", "json")
	got := runOnlineCLI(t, harness, bearerPath, true, "principal", "get", principalID)
	results = append(results, listed, got)
	assert.Contains(t, string(listed.Stdout), principalID)
	assert.Contains(t, string(got.Stdout), "CLI principal")

	updated := runOnlineCLI(t, harness, bearerPath, true, "principal", "update", principalID, "--display-name", "CLI principal updated", "--output", "json")
	results = append(results, updated)
	var principal contract.Principal
	require.NoError(t, json.Unmarshal(updated.Stdout, &principal))
	assert.Equal(t, "CLI principal updated", principal.DisplayName)
	oldETag := etag
	etag = contract.PrincipalETag(principalID, principal.Revision)

	refused := runOnlineCLI(t, harness, bearerPath, false, "principal", "update", principalID, "--etag", etag, "--state", "disabled", "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	disabled := runOnlineCLI(t, harness, bearerPath, true, "principal", "update", principalID, "--etag", etag, "--state", "disabled", "--yes", "--output", "json")
	results = append(results, disabled)
	require.NoError(t, json.Unmarshal(disabled.Stdout, &principal))
	assert.Equal(t, contract.PrincipalDisabled, principal.State)
	etag = contract.PrincipalETag(principalID, principal.Revision)

	stale := runOnlineCLI(t, harness, bearerPath, false, "principal", "update", principalID, "--etag", oldETag, "--display-name", "stale", "--output", "json")
	noOp := runOnlineCLI(t, harness, bearerPath, false, "principal", "update", principalID, "--etag", etag, "--state", "disabled", "--yes", "--output", "json")
	results = append(results, stale, noOp)
	assert.Equal(t, 5, stale.ExitCode)
	assert.Equal(t, 5, noOp.ExitCode)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "principal", "update", principalID, "--etag", etag, "--display-name", "uncertain", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.Contains(t, string(uncertain.Stderr), "Nothing was replayed or overwritten")

	harness.Stop(syscall.SIGTERM)
	preHandoff := runOnlineCLI(t, harness, bearerPath, false, "principal", "get", principalID, "--output", "json")
	results = append(results, preHandoff)
	assert.Equal(t, 9, preHandoff.ExitCode)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T46 must own one Gateway lifecycle")
}
