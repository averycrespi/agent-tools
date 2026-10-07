package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEncryptedCustodyArtifactCannotRestoreIntoLegacyInstallation(t *testing.T) {
	manager, store, owner := newBackupManager(t, nil)
	require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, manager.clock))
	// Deliberately construct an otherwise valid unsupported artifact without the
	// public creation gate, as if supplied by an external copier/newer writer.
	artifact := legacyArtifact(t, manager, nil)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM secret_custody`); return err }))
	require.NoError(t, os.Remove(filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName)))
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	for _, securityOnly := range []bool{false, true} {
		_, err := Restore(t.Context(), RestoreOptions{Root: owner.Layout().Root, BackupID: artifact.ID, SecurityOnly: securityOnly, Sink: new(captureSink), Clock: manager.clock, Entropy: rand.Reader})
		require.ErrorIs(t, err, ErrEncryptedCustodyUnsupported)
	}
}

func TestEncryptedCustodyRefusesLegacyReplayAndRestoreWithoutEffects(t *testing.T) {
	manager, store, owner := newBackupManager(t, nil)
	legacy, _, err := manager.Create(t.Context(), "authority", "before-setup")
	require.NoError(t, err)
	require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, manager.clock))
	before, err := os.ReadDir(owner.Layout().Backups)
	require.NoError(t, err)
	for _, key := range []string{"before-setup"} {
		_, replay, err := manager.Create(t.Context(), "authority", key)
		require.ErrorIs(t, err, ErrEncryptedCustodyUnsupported)
		require.False(t, replay)
	}
	after, err := os.ReadDir(owner.Layout().Backups)
	require.NoError(t, err)
	require.Len(t, after, len(before))
	_, err = manager.Get(t.Context(), legacy.ID)
	require.NoError(t, err)
	_, err = manager.List(t.Context())
	require.NoError(t, err)
	root := owner.Layout().Root
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	original, err := os.ReadFile(filepath.Join(root, "gateway.db"))
	require.NoError(t, err)
	for _, securityOnly := range []bool{false, true} {
		sink := new(captureSink)
		_, err = Restore(t.Context(), RestoreOptions{Root: root, BackupID: legacy.ID, SecurityOnly: securityOnly, Sink: sink, Clock: manager.clock, Entropy: rand.Reader, Before: func(_ctx context.Context, _owner *gatewaypaths.Ownership) error {
			t.Fatal("incompatible restore reached approval")
			return nil
		}})
		require.ErrorIs(t, err, ErrEncryptedCustodyUnsupported)
		require.Empty(t, sink.bearer)
	}
	actual, err := os.ReadFile(filepath.Join(root, "gateway.db"))
	require.NoError(t, err)
	require.Equal(t, original, actual)
	_, err = os.Lstat(filepath.Join(root, "gateway.db.restore"))
	require.ErrorIs(t, err, os.ErrNotExist)
	stopped, err := gatewaypaths.AcquireStoppedExisting(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, stopped.Close()) }()
	_, err = InspectRestoreScope(t.Context(), stopped, legacy.ID, true)
	require.ErrorIs(t, err, ErrEncryptedCustodyUnsupported)
	_, err = storage.InspectMaintenance(t.Context(), stopped, nil)
	require.NoError(t, err)
}
