//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIPrincipalCredentials(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	dir := t.TempDir()
	principalPath := filepath.Join(dir, "principal.json")
	require.NoError(t, os.WriteFile(principalPath, []byte(`{"display_name":"Credential principal","visibility":"all"}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 10)

	created := runOnlineCLI(t, harness, bearerPath, true, "principal", "create", "--file", principalPath, "--output", "json")
	results = append(results, created)
	var creation contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Stdout, &creation))
	principalID := creation.Principal.ID
	originalETag := contract.PrincipalETag(principalID, creation.Principal.Revision)

	refusedPath := filepath.Join(dir, "refused")
	refused := runOnlineCLI(t, harness, bearerPath, false, "principal", "credential", "issue", principalID, "--etag", originalETag, "--secret-output", refusedPath, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	assert.NoFileExists(t, refusedPath)

	firstPath := filepath.Join(dir, "first")
	issued := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "issue", principalID, "--etag", originalETag, "--secret-output", firstPath, "--yes", "--output", "json")
	results = append(results, issued)
	var principal contract.Principal
	require.NoError(t, json.Unmarshal(issued.Stdout, &principal))
	require.NotNil(t, principal.Credential)
	assert.NotContains(t, string(issued.Stdout), contract.AgentBearerPrefix)
	firstRaw, err := os.ReadFile(firstPath)
	require.NoError(t, err)
	firstBearer := newAgentBearer(t, strings.TrimSpace(string(firstRaw)))
	defer firstBearer.destroy()
	etag := contract.PrincipalETag(principalID, principal.Revision)

	stalePath := filepath.Join(dir, "stale")
	stale := runOnlineCLI(t, harness, bearerPath, false, "principal", "credential", "issue", principalID, "--etag", originalETag, "--secret-output", stalePath, "--yes", "--output", "json")
	results = append(results, stale)
	assert.Equal(t, 5, stale.ExitCode)
	assert.NoFileExists(t, stalePath)

	secondPath := filepath.Join(dir, "second")
	replaced := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "issue", principalID, "--etag", etag, "--secret-output", secondPath, "--yes", "--output", "json")
	results = append(results, replaced)
	require.NoError(t, json.Unmarshal(replaced.Stdout, &principal))
	secondRaw, err := os.ReadFile(secondPath)
	require.NoError(t, err)
	secondBearer := newAgentBearer(t, strings.TrimSpace(string(secondRaw)))
	defer secondBearer.destroy()
	assert.Equal(t, http.StatusUnauthorized, harness.ModernDiscover(firstBearer, json.RawMessage(`1`)).StatusCode)
	assert.Equal(t, http.StatusOK, harness.ModernDiscover(secondBearer, json.RawMessage(`2`)).StatusCode)
	etag = contract.PrincipalETag(principalID, principal.Revision)

	revokeRefused := runOnlineCLI(t, harness, bearerPath, false, "principal", "credential", "revoke", principalID, "--etag", etag, "--output", "json")
	results = append(results, revokeRefused)
	assert.Equal(t, 2, revokeRefused.ExitCode)
	revoked := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "revoke", principalID, "--etag", etag, "--yes", "--output", "json")
	results = append(results, revoked)
	require.NoError(t, json.Unmarshal(revoked.Stdout, &principal))
	assert.Nil(t, principal.Credential)
	assert.Equal(t, http.StatusUnauthorized, harness.ModernDiscover(secondBearer, json.RawMessage(`3`)).StatusCode)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fake.Close()
	uncertainPath := filepath.Join(dir, "uncertain")
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "principal", "credential", "issue", principalID, "--etag", contract.PrincipalETag(principalID, principal.Revision), "--secret-output", uncertainPath, "--yes", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.NoFileExists(t, uncertainPath)
	assert.Contains(t, string(uncertain.Stderr), "cannot recover its bearer")

	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(string(firstRaw)))
		assert.NotContains(t, string(result.Stderr), strings.TrimSpace(string(firstRaw)))
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(string(secondRaw)))
		assert.NotContains(t, string(result.Stderr), strings.TrimSpace(string(secondRaw)))
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T47 must own one Gateway lifecycle")
}
