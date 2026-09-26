//go:build darwin || linux

package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSQLiteSidecarPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0640, 0604, 0644, 0660, 0666, 0755, 0400} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			path := filepath.Join(root, "gateway.db-wal")
			require.NoError(t, os.WriteFile(path, []byte("retained WAL"), 0600))
			require.NoError(t, os.Chmod(path, mode))
			err := ValidateSQLiteSidecar(path)
			if mode & ^os.FileMode(0644) == 0 && mode&0600 == 0600 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrUnsafePath)
			}
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, "retained WAL", string(data))
			info, statErr := os.Lstat(path)
			require.NoError(t, statErr)
			require.Equal(t, mode, info.Mode().Perm(), "validation must not chmod")
		})
	}
}

func TestSQLiteSidecarRequiresPrivateDirectoryAndRegularOwnedFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(root, "gateway.db-shm")
	require.NoError(t, os.WriteFile(path, nil, 0644))
	require.NoError(t, os.Chmod(root, 0755))
	require.ErrorIs(t, ValidateSQLiteSidecar(path), ErrUnsafePath)
	require.NoError(t, os.Chmod(root, 0700))
	linked := filepath.Join(root, "linked.db-shm")
	require.NoError(t, os.Symlink(path, linked))
	require.ErrorIs(t, ValidateSQLiteSidecar(linked), ErrUnsafePath)
	hard := filepath.Join(root, "hard.db-shm")
	require.NoError(t, os.Link(path, hard))
	require.ErrorIs(t, ValidateSQLiteSidecar(hard), ErrUnsafePath)
	require.ErrorIs(t, ValidateOwnerOnlyFile(path), ErrUnsafePath, "private file rules remain strict")
}

func TestStoppedOwnershipAcceptsReadOnlySharedSidecars(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := Acquire(root)
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	for _, suffix := range []string{"-wal", "-shm"} {
		path := filepath.Join(root, DatabaseName+suffix)
		require.NoError(t, os.WriteFile(path, nil, 0600))
		require.NoError(t, os.Chmod(path, 0644))
	}
	owner, err = AcquireStoppedExisting(root)
	require.NoError(t, err)
	require.NoError(t, owner.Close())
}
