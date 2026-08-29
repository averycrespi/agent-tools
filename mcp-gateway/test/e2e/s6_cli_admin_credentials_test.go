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

func TestS6CLIAdminCredentials(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "create.json")
	invalidPath := filepath.Join(dir, "invalid.json")
	secretPath := filepath.Join(dir, "new-bearer")
	require.NoError(t, os.WriteFile(inputPath, []byte(`{"expires_at":null}`), 0o600))
	require.NoError(t, os.WriteFile(invalidPath, []byte(`{"expires_at":null,"unknown":true}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 9)

	listed := runOnlineCLI(t, harness, bearerPath, true, "admin-credential", "list", "--limit", "10", "--output", "json")
	results = append(results, listed)
	var page contract.Collection[contract.AdminCredential]
	require.NoError(t, json.Unmarshal(listed.Stdout, &page))
	require.Len(t, page.Items, 1)
	originalID := page.Items[0].ID
	got := runOnlineCLI(t, harness, bearerPath, true, "admin-credential", "get", originalID)
	results = append(results, got)
	assert.Contains(t, string(got.Stdout), originalID)

	invalid := runOnlineCLI(t, harness, bearerPath, false, "admin-credential", "create", "--file", invalidPath, "--secret-output", filepath.Join(dir, "unused"), "--output", "json")
	results = append(results, invalid)
	assert.Equal(t, 2, invalid.ExitCode)
	terminalRefused := runOnlineCLI(t, harness, bearerPath, false, "admin-credential", "create", "--file", inputPath, "--output", "json")
	results = append(results, terminalRefused)
	assert.Equal(t, 2, terminalRefused.ExitCode)

	created := runOnlineCLI(t, harness, bearerPath, true, "admin-credential", "create", "--file", inputPath, "--secret-output", secretPath, "--output", "json")
	results = append(results, created)
	var credential contract.AdminCredential
	require.NoError(t, json.Unmarshal(created.Stdout, &credential))
	assert.NotEmpty(t, credential.ID)
	assert.NotContains(t, string(created.Stdout), "mgw_admin_")
	secretInfo, err := os.Stat(secretPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), secretInfo.Mode().Perm())
	newBearer, err := os.ReadFile(secretPath)
	require.NoError(t, err)
	assert.Regexp(t, `^mgw_admin_[A-Za-z0-9_-]{43}\n$`, string(newBearer))
	newBearerPath := filepath.Join(dir, "new-bearer-input")
	require.NoError(t, os.WriteFile(newBearerPath, newBearer, 0o600))

	refused := runOnlineCLI(t, harness, bearerPath, false, "admin-credential", "revoke", credential.ID, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	revoked := runOnlineCLI(t, harness, bearerPath, true, "admin-credential", "revoke", credential.ID, "--yes", "--output", "json")
	results = append(results, revoked)
	assert.JSONEq(t, `{}`, string(revoked.Stdout))
	revokedGet := runOnlineCLI(t, harness, bearerPath, true, "admin-credential", "get", credential.ID, "--output", "json")
	results = append(results, revokedGet)
	assert.Contains(t, string(revokedGet.Stdout), `"status":"revoked"`)
	revokedAuthority := runOnlineCLI(t, harness, newBearerPath, false, "admin-credential", "list", "--output", "json")
	results = append(results, revokedAuthority)
	assert.Equal(t, 3, revokedAuthority.ExitCode)
	last := runOnlineCLI(t, harness, bearerPath, false, "admin-credential", "revoke", originalID, "--yes", "--output", "json")
	results = append(results, last)
	assert.Equal(t, 5, last.ExitCode)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fake.Close()
	uncertainPath := filepath.Join(dir, "uncertain-bearer")
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "admin-credential", "create", "--file", inputPath, "--secret-output", uncertainPath, "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.NoFileExists(t, uncertainPath)
	assert.Contains(t, string(uncertain.Stderr), "Nothing was replayed")

	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(string(newBearer)))
		assert.NotContains(t, string(result.Stderr), strings.TrimSpace(string(newBearer)))
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T44 must own one Gateway lifecycle")
}
