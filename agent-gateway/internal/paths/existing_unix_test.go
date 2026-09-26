//go:build darwin || linux

package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExistingStartupPreservesCrashSidecarsForStorageOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := Acquire(root)
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	sidecar := filepath.Join(root, DatabaseName) + "-shm"
	require.NoError(t, os.WriteFile(sidecar, []byte("crash-sidecar"), 0644))
	require.NoError(t, os.Chmod(sidecar, 0644))
	_, err = AcquireStoppedExisting(root)
	require.ErrorIs(t, err, ErrUnsafePath)
	owner, err = AcquireExisting(root)
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	value, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	require.Equal(t, "crash-sidecar", string(value))
	info, err := os.Stat(sidecar)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	absent := filepath.Join(t.TempDir(), "absent")
	_, err = AcquireExisting(absent)
	require.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(absent)
	require.ErrorIs(t, err, os.ErrNotExist)
}
