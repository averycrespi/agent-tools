package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestSecurityStagingRefusalDoesNotPublishAuthority(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	limit, _ := contract.FixedLimitByName("database_bytes")
	refusal := func(_ string, size int64) (func(), error) {
		require.Equal(t, 4*limit.Maximum, size)
		return nil, ErrResourceLimit
	}
	manager.reserve = refusal
	_, _, err := manager.Create(t.Context(), "authority", "refused")
	require.ErrorIs(t, err, ErrResourceLimit)
	entries, err := os.ReadDir(owner.Layout().Backups)
	require.NoError(t, err)
	require.Empty(t, entries)
	manager.reserve = nil
	artifact, _, err := manager.Create(t.Context(), "authority", "prepared")
	require.NoError(t, err)
	layout := owner.Layout()
	require.NoError(t, control.Close())
	require.NoError(t, owner.Close())
	before, err := os.ReadFile(layout.Database)
	require.NoError(t, err)
	sink := new(captureSink)
	_, err = Restore(t.Context(), RestoreOptions{Root: layout.Root, BackupID: artifact.ID, Sink: sink, Clock: manager.clock, Entropy: bytes.NewReader(restoreTestEntropy(9, 1024)), reserve: refusal})
	require.ErrorIs(t, err, ErrResourceLimit)
	require.Empty(t, sink.bearer)
	after, err := os.ReadFile(layout.Database)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = os.Lstat(filepath.Join(layout.Root, "gateway.db.restore"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
