package paths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireCreatesOwnerOnlyCanonicalLayout(t *testing.T) {
	parent := t.TempDir()
	ownership, err := Acquire(filepath.Join(parent, "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })

	layout := ownership.Layout()
	resolvedParent, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolvedParent, "gateway"), layout.Root)
	assert.Equal(t, filepath.Join(layout.Root, DatabaseName), layout.Database)
	assert.Equal(t, filepath.Join(layout.Root, LockName), layout.Lock)
	assert.Equal(t, filepath.Join(layout.Root, RunMarkerName), layout.RunMarker)
	assert.Equal(t, filepath.Join(layout.Root, MutationMarkerName), layout.MutationMarker)
	assert.Equal(t, filepath.Join(layout.Root, BackupsName), layout.Backups)
	assert.False(t, ownership.WasUnclean())

	assertMode(t, layout.Root, 0o700)
	assertMode(t, layout.Lock, 0o600)
	assertMode(t, layout.RunMarker, 0o600)
}

func TestAcquireRejectsUnsafeRootsAndFiles(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		realRoot := filepath.Join(t.TempDir(), "real")
		require.NoError(t, os.Mkdir(realRoot, 0o700))
		link := filepath.Join(t.TempDir(), "link")
		require.NoError(t, os.Symlink(realRoot, link))

		_, err := Acquire(link)
		assert.ErrorIs(t, err, ErrUnsafePath)
	})

	t.Run("root permissions", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "gateway")
		require.NoError(t, os.Mkdir(root, 0o755))

		_, err := Acquire(root)
		assert.ErrorIs(t, err, ErrUnsafePath)
	})

	t.Run("lock symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "gateway")
		require.NoError(t, os.Mkdir(root, 0o700))
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, nil, 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(root, LockName)))

		_, err := Acquire(root)
		assert.ErrorIs(t, err, ErrUnsafePath)
	})

	t.Run("marker permissions", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "gateway")
		require.NoError(t, os.Mkdir(root, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, RunMarkerName), []byte("running\n"), 0o644))

		_, err := Acquire(root)
		assert.ErrorIs(t, err, ErrUnsafePath)
	})
}

func TestS6CLISensitiveSinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "prepared-secret")
	file, err := CreateOwnerOnlyFile(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	assertMode(t, path, 0o600)
	require.NoError(t, ValidateOwnerOnlyFile(path))

	_, err = CreateOwnerOnlyFile(path)
	assert.Error(t, err, "an existing output must never be overwritten")
	target := filepath.Join(root, "target")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(target, link))
	_, err = CreateOwnerOnlyFile(link)
	assert.Error(t, err)
	info, statErr := os.Lstat(link)
	require.NoError(t, statErr)
	assert.True(t, info.Mode()&os.ModeSymlink != 0)
}

func TestAcquireIsExclusiveAcrossCanonicalAliases(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "gateway")
	first, err := Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })

	aliasParent := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(parent, aliasParent))
	_, err = Acquire(filepath.Join(aliasParent, "gateway"))
	assert.ErrorIs(t, err, ErrInUse)
}

func TestRunMarkerDistinguishesCleanAndUncleanRelease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	first, err := Acquire(root)
	require.NoError(t, err)
	assert.False(t, first.WasUnclean())
	require.NoError(t, first.Close())

	second, err := Acquire(root)
	require.NoError(t, err)
	assert.True(t, second.WasUnclean())
	require.NoError(t, second.MarkClean())
	require.NoError(t, second.Close())

	third, err := Acquire(root)
	require.NoError(t, err)
	assert.False(t, third.WasUnclean())
	require.NoError(t, third.MarkClean())
	require.NoError(t, third.Close())
}

func TestStoppedProcessMaintenanceUsesExclusiveOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	service, err := Acquire(root)
	require.NoError(t, err)

	_, err = AcquireForMaintenance(root)
	assert.ErrorIs(t, err, ErrInUse)
	require.NoError(t, service.Close())

	maintenance, err := AcquireForMaintenance(root)
	require.NoError(t, err)
	require.NoError(t, maintenance.MarkClean())
	require.NoError(t, maintenance.Close())
}

func TestCloseAndMarkCleanAreIdempotent(t *testing.T) {
	ownership, err := Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	require.NoError(t, ownership.MarkClean())
	require.NoError(t, ownership.MarkClean())
	require.NoError(t, ownership.Close())
	require.NoError(t, ownership.Close())

	err = ownership.MarkClean()
	assert.True(t, errors.Is(err, ErrClosed))
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}
