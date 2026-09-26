package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestRestoreInspectionAndExecutionRejectArtifactSidecars(t *testing.T) {
	for _, file := range []string{databaseFile, "traffic.db"} {
		for _, suffix := range []string{"-wal", "-journal"} {
			t.Run(file+suffix, func(t *testing.T) {
				manager, store, owner := newBackupManager(t, nil)
				artifact, _, err := manager.Create(t.Context(), "authority", "sidecar-test")
				require.NoError(t, err)
				expected, err := InspectRestore(t.Context(), owner, artifact.ID)
				require.NoError(t, err)
				path := filepath.Join(owner.Layout().Backups, artifact.ID, file+suffix)
				require.NoError(t, os.WriteFile(path, []byte("uncheckpointed evidence"), 0600))
				_, err = InspectRestore(t.Context(), owner, artifact.ID)
				require.ErrorIs(t, err, storage.ErrInspectionUnavailable)
				require.ErrorIs(t, expected.Revalidate(t.Context(), owner), storage.ErrInspectionUnavailable)
				require.NoError(t, store.Close())
				layout := owner.Layout()
				require.NoError(t, owner.Close())
				before, err := os.ReadFile(layout.Database)
				require.NoError(t, err)
				secret := filepath.Join(t.TempDir(), "replacement")
				_, err = Restore(t.Context(), RestoreOptions{Root: layout.Root, BackupID: artifact.ID, Sink: admin.NewFileSecretSink(secret), Clock: manager.clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x66}, 1024))})
				require.ErrorIs(t, err, ErrInvalidArtifact)
				after, err := os.ReadFile(layout.Database)
				require.NoError(t, err)
				require.Equal(t, before, after)
				for _, absent := range []string{secret, layout.Database + ".restore"} {
					_, err := os.Lstat(absent)
					require.ErrorIs(t, err, os.ErrNotExist)
				}
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "uncheckpointed evidence", string(data))
			})
		}
	}
}
