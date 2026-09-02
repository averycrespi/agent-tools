//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIBackups(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	results := make([]testutil.ProcessResult, 0, 7)

	created := runOnlineCLI(t, harness, bearerPath, true, "backup", "create", "--idempotency-key", "backup-once", "--output", "json")
	results = append(results, created)
	var backup contract.Backup
	require.NoError(t, json.Unmarshal(created.Stdout, &backup))
	assert.NotEmpty(t, backup.ID)
	assert.Positive(t, backup.SizeBytes)

	replayed := runOnlineCLI(t, harness, bearerPath, true, "backup", "create", "--idempotency-key", "backup-once", "--output", "json")
	results = append(results, replayed)
	var replay contract.Backup
	require.NoError(t, json.Unmarshal(replayed.Stdout, &replay))
	assert.Equal(t, backup.ID, replay.ID)

	listed := runOnlineCLI(t, harness, bearerPath, true, "backup", "list", "--limit", "10", "--output", "json")
	got := runOnlineCLI(t, harness, bearerPath, true, "backup", "get", backup.ID)
	results = append(results, listed, got)
	assert.Contains(t, string(listed.Stdout), backup.ID)
	assert.Contains(t, string(got.Stdout), backup.SHA256)

	refused := runOnlineCLI(t, harness, bearerPath, false, "backup", "delete", backup.ID, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	deleted := runOnlineCLI(t, harness, bearerPath, true, "backup", "delete", backup.ID, "--yes", "--output", "json")
	results = append(results, deleted)
	assert.JSONEq(t, `{}`, string(deleted.Stdout))
	missing := runOnlineCLI(t, harness, bearerPath, false, "backup", "get", backup.ID, "--output", "json")
	results = append(results, missing)
	assert.Equal(t, 4, missing.ExitCode)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "backup", "create", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.Contains(t, string(uncertain.Stderr), "Nothing was replayed")
	assert.Regexp(t, regexp.MustCompile(`idempotency key [A-Za-z0-9_-]{32}`), string(uncertain.Stderr))
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
	assert.Len(t, harness.results, 1, "T45 must own one Gateway lifecycle")
}
