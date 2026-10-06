package backup

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestSecurityBackupRestoreDoesNotInspectOptionalHistory(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unselected", true: "selected"}[selected], func(t *testing.T) {
			manager, control, owner := newBackupManager(t, nil)
			sink := new(captureSink)
			_, err := admin.NewService(control, manager.clock, bytes.NewReader(restoreTestEntropy(1, 512))).Initialize(t.Context(), sink)
			require.NoError(t, err)
			generation := "01ARZ3NDEKTSV4RRFFQ69G5FA0"
			if selected {
				require.NoError(t, control.SelectTraffic(t.Context(), "", generation))
			}
			layout := owner.Layout()
			unrelated := filepath.Join(layout.Root, "unrelated-retained-link")
			require.NoError(t, os.Symlink("missing-original", unrelated))
			history := filepath.Join(layout.Root, "traffic-"+generation+".db")
			require.NoError(t, os.WriteFile(history, []byte("corrupt optional history"), 0600))
			require.NoError(t, os.WriteFile(history+"-wal", []byte("retained journal evidence"), 0600))
			artifact, _, err := manager.Create(t.Context(), "authority", "security")
			require.NoError(t, err)
			require.Equal(t, "omitted", artifact.History)
			directory := filepath.Join(layout.Backups, artifact.ID)
			metadata, err := os.ReadFile(filepath.Join(directory, metadataFile))
			require.NoError(t, err)
			var item artifactMetadata
			require.NoError(t, json.Unmarshal(metadata, &item))
			require.Equal(t, 3, item.Format)
			_, err = os.Stat(filepath.Join(directory, "traffic.db"))
			require.ErrorIs(t, err, os.ErrNotExist)
			// Unrelated history artifacts and sidecars are not security mutation targets.
			require.NoError(t, os.WriteFile(filepath.Join(directory, "traffic.db-wal"), []byte("preserve"), 0600))
			require.NoError(t, control.Checkpoint(t.Context()))
			inspected, err := InspectRestore(t.Context(), owner, artifact.ID)
			require.NoError(t, err)
			require.Equal(t, "omitted-not-verified", inspected.History)
			require.NoError(t, control.Close())
			require.NoError(t, owner.Close())
			replacement := new(captureSink)
			identity, err := Restore(t.Context(), RestoreOptions{Root: layout.Root, BackupID: artifact.ID, Sink: replacement, Clock: manager.clock, Entropy: bytes.NewReader(restoreTestEntropy(2, 1024))})
			require.NoError(t, err)
			require.Empty(t, identity.TrafficGeneration)
			require.NotEmpty(t, replacement.bearer)
			require.NotEqual(t, sink.bearer, replacement.bearer)
			preserved, err := os.ReadFile(history)
			require.NoError(t, err)
			require.Equal(t, "corrupt optional history", string(preserved))
			preserved, err = os.ReadFile(history + "-wal")
			require.NoError(t, err)
			require.Equal(t, "retained journal evidence", string(preserved))
			installed, err := storage.VerifyBackup(t.Context(), layout.Database)
			require.NoError(t, err)
			require.Empty(t, installed.TrafficGeneration)
			link, err := os.Readlink(unrelated)
			require.NoError(t, err)
			require.Equal(t, "missing-original", link)
		})
	}
}

func TestSecurityBackupOmitsEmbeddedHistoryWithoutChangingSource(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	require.NoError(t, control.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO invocations
   (id,principal_id,credential_id,credential_fingerprint,credential_revision,admitted_at,admission_class)
   VALUES(?,?,?,'0123456789abcdef',1,'2026-08-22T12:00:00.000000000Z','invalid_params')`, backupTestInstallationID, backupTestInstallationID, backupTestInstallationID)
		return err
	}))
	created, _, err := manager.Create(t.Context(), "authority", "embedded")
	require.NoError(t, err)
	require.NoError(t, invocation.VerifyOmittedLegacyTraffic(t.Context(), filepath.Join(owner.Layout().Backups, created.ID, databaseFile)))
	require.NoError(t, control.View(t.Context(), func(tx *sql.Tx) error {
		var count int
		err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM invocations`).Scan(&count)
		require.Equal(t, 1, count)
		return err
	}))
}

func TestSecurityImportDoesNotClaimDamagedPairValid(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	generation := "01ARZ3NDEKTSV4RRFFQ69G5FA0"
	traffic, err := invocation.CreateTraffic(t.Context(), owner, backupTestInstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, traffic.Close()) }()
	require.NoError(t, control.SelectTraffic(t.Context(), "", generation))
	_, err = admin.NewService(control, manager.clock, bytes.NewReader(restoreTestEntropy(3, 512))).Initialize(t.Context(), new(captureSink))
	require.NoError(t, err)
	artifact := legacyArtifact(t, manager, traffic)
	path := filepath.Join(owner.Layout().Backups, artifact.ID, "traffic.db")
	require.NoError(t, os.WriteFile(path, []byte("damaged retained history"), 0600))
	_, err = manager.Get(t.Context(), artifact.ID)
	require.Error(t, err)
	_, err = InspectRestore(t.Context(), owner, artifact.ID)
	require.Error(t, err)
	require.NoError(t, control.Checkpoint(t.Context()))
	inspection, err := InspectRestoreScope(t.Context(), owner, artifact.ID, true)
	require.NoError(t, err)
	require.Equal(t, "omitted-not-verified", inspection.History)
	require.NoError(t, inspection.Revalidate(t.Context(), owner))
	preserved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "damaged retained history", string(preserved))
	layout := owner.Layout()
	require.NoError(t, traffic.Close())
	require.NoError(t, control.Close())
	require.NoError(t, owner.Close())
	identity, err := Restore(t.Context(), RestoreOptions{Root: layout.Root, BackupID: artifact.ID, SecurityOnly: true, Sink: new(captureSink), Clock: manager.clock, Entropy: bytes.NewReader(restoreTestEntropy(4, 1024))})
	require.NoError(t, err)
	require.Empty(t, identity.TrafficGeneration)
	preserved, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "damaged retained history", string(preserved))
}
