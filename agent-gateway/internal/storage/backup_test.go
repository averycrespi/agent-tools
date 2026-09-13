package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyClosedBackupDoesNotCreateSidecars(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	directory := t.TempDir()
	path := filepath.Join(directory, "gateway.db")
	require.NoError(t, store.BackupTo(t.Context(), path))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	identity, err := VerifyBackup(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, testInstallationID, identity.InstallationID)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "gateway.db", entries[0].Name())
}
