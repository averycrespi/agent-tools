package backup

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
)

func TestSecurityImportOmitsLegacyEmbeddedRowsAndPreservesOriginal(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	require.NoError(t, control.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO invocations (id,principal_id,credential_id,credential_fingerprint,credential_revision,admitted_at,admission_class) VALUES(?,?,?,'0123456789abcdef',1,'2026-08-22T12:00:00.000000000Z','invalid_params')`, backupTestInstallationID, backupTestInstallationID, backupTestInstallationID)
		return err
	}))
	_, err := admin.NewService(control, manager.clock, bytes.NewReader(restoreTestEntropy(2, 512))).Initialize(t.Context(), new(captureSink))
	require.NoError(t, err)
	artifact := legacyArtifact(t, manager, nil)
	layout := owner.Layout()
	originalPath := filepath.Join(layout.Backups, artifact.ID, databaseFile)
	original, err := os.ReadFile(originalPath)
	require.NoError(t, err)
	require.Error(t, invocation.VerifyOmittedLegacyTraffic(t.Context(), originalPath))
	require.NoError(t, control.Checkpoint(t.Context()))
	inspection, err := InspectRestoreScope(t.Context(), owner, artifact.ID, true)
	require.NoError(t, err)
	require.Equal(t, "omitted-not-verified", inspection.History)
	require.NoError(t, control.Close())
	require.NoError(t, owner.Close())
	result, err := Restore(t.Context(), RestoreOptions{Root: layout.Root, BackupID: artifact.ID, SecurityOnly: true, Sink: new(captureSink), Clock: manager.clock, Entropy: bytes.NewReader(restoreTestEntropy(6, 1024))})
	require.NoError(t, err)
	require.Empty(t, result.TrafficGeneration)
	require.NoError(t, invocation.VerifyOmittedLegacyTraffic(t.Context(), layout.Database))
	retained, err := os.ReadFile(originalPath)
	require.NoError(t, err)
	require.Equal(t, original, retained)
}
