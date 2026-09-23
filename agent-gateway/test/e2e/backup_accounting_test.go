//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryStatusUsesMetadataOnlyBackupAccounting(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	check := func(count int64) {
		t.Helper()
		response := harness.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil)
		var status contract.SystemStatus
		decodeSnapshot(t, response, http.StatusOK, &status)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		require.Equal(t, count, status.Limits.BackupRecords.InUse)
		require.Equal(t, count, status.Limits.IdempotencyRecords.InUse)
	}
	create := func(key string) contract.Backup {
		t.Helper()
		response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v2/backups", []byte(`{}`), map[string]string{"Idempotency-Key": key})
		var artifact contract.Backup
		decodeSnapshot(t, response, http.StatusCreated, &artifact)
		return artifact
	}
	check(0)
	first := create("accounting-first")
	check(1)
	response := harness.adminSnapshot(http.MethodDelete, "/api/v2/backups/"+first.ID, []byte(`{}`))
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	check(0)
	artifact := create("accounting-second")
	check(1)
	directory := filepath.Join(harness.root, "backups", artifact.ID)
	// This exercises the real serving closure, not a configured status mock.
	// Removing both database files makes any verification/content-read path fail.
	require.NoError(t, os.Remove(filepath.Join(directory, "gateway.db")))
	require.NoError(t, os.Remove(filepath.Join(directory, "traffic.db")))
	check(1)
	response = harness.adminSnapshot(http.MethodGet, "/api/v2/backups/"+artifact.ID, nil)
	require.NotEqual(t, http.StatusOK, response.StatusCode)
	require.NoError(t, os.Remove(filepath.Join(directory, "metadata.json")))
	response = harness.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil)
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Contains(t, string(response.Body), `"code":"storage_unavailable"`)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	harness.Stop(os.Interrupt)
}
