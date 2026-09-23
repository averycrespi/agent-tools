//go:build darwin || linux

package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestAccountingRejectsUnsafeFilesystem(t *testing.T) {
	manager, _, owner := newBackupManager(t, nil)
	created, _, err := manager.Create(t.Context(), "authority", "unsafe-filesystem")
	require.NoError(t, err)
	directory := filepath.Join(owner.Layout().Backups, created.ID)
	path := filepath.Join(directory, metadataFile)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, unix.Mkfifo(path, 0o600))
	_, _, err = manager.AccountingStatus(t.Context())
	require.ErrorIs(t, err, ErrInvalidArtifact)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, original, 0o600))
	require.NoError(t, os.Chmod(directory, 0o755))
	_, _, err = manager.AccountingStatus(t.Context())
	require.ErrorIs(t, err, ErrInvalidArtifact)
	require.NoError(t, os.Chmod(directory, 0o700))
	require.NoError(t, os.Chmod(owner.Layout().Backups, 0o755))
	_, _, err = manager.AccountingStatus(t.Context())
	require.ErrorIs(t, err, ErrInvalidArtifact)
	require.NoError(t, os.Chmod(owner.Layout().Backups, 0o700))
	retained := filepath.Join(owner.Layout().Backups, ".retained")
	require.NoError(t, os.Rename(directory, retained))
	require.NoError(t, os.Symlink(retained, directory))
	_, _, err = manager.AccountingStatus(t.Context())
	require.ErrorIs(t, err, ErrInvalidArtifact)
}
