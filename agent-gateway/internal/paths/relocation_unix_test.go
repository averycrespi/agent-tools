//go:build darwin || linux

package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func relocationFixture(t *testing.T) (string, string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(parent, 0o700))
	source := filepath.Join(parent, LegacyInstallationName)
	destination := filepath.Join(parent, InstallationName)
	owner, err := Acquire(source)
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	for _, name := range []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm", AdminBearerName, MutationMarkerName} {
		require.NoError(t, os.WriteFile(filepath.Join(source, name), []byte("preserve "+name), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(source, BackupsName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, BackupsName, "historical"), []byte("preserve backup"), 0o600))
	return source, destination
}

func TestRelocationPreservesWholeRootAndLock(t *testing.T) {
	source, destination := relocationFixture(t)
	before, err := os.Stat(source)
	require.NoError(t, err)
	relocation, err := InspectRelocation(source, destination)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relocation.Close()) })
	_, err = os.Lstat(destination)
	require.ErrorIs(t, err, os.ErrNotExist, "preflight must not reserve destination")
	require.NoError(t, relocation.Commit())
	after, err := os.Stat(destination)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.True(t, RelocationCompleted(source, destination))
	_, err = Acquire(source)
	require.ErrorIs(t, err, ErrUnsafePath, "tombstone blocks even legacy Prepare")
	_, err = Acquire(destination)
	require.ErrorIs(t, err, ErrInUse, "moved lock remains held")
	for _, name := range []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm", AdminBearerName, MutationMarkerName} {
		data, readErr := os.ReadFile(filepath.Join(destination, name))
		require.NoError(t, readErr)
		require.Equal(t, "preserve "+name, string(data))
	}
	marker, err := os.ReadFile(filepath.Join(destination, RunMarkerName))
	require.NoError(t, err)
	require.Equal(t, "running\n", string(marker))
	backup, err := os.ReadFile(filepath.Join(destination, BackupsName, "historical"))
	require.NoError(t, err)
	require.Equal(t, "preserve backup", string(backup))
	layout, err := resolveInstallation("", filepath.Dir(source), nil)
	require.NoError(t, err)
	require.Equal(t, destination, layout.Root)
	require.NoError(t, relocation.Close())
	owner, err := Acquire(destination)
	require.NoError(t, err)
	require.True(t, owner.WasUnclean(), "migration must not clear markers")
	require.NoError(t, owner.Close())
}

func TestRelocationRefusesUnsafeAndRunningOwners(t *testing.T) {
	for _, mode := range []string{"running", "destination", "incomplete-reservation", "public-file", "link", "hard-link", "nested", "parent-link"} {
		t.Run(mode, func(t *testing.T) {
			source, destination := relocationFixture(t)
			switch mode {
			case "running":
				owner, err := Acquire(source)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, owner.Close()) })
			case "destination":
				require.NoError(t, os.Mkdir(destination, 0o700))
			case "incomplete-reservation":
				require.NoError(t, os.WriteFile(destination, []byte(relocationMagic), 0o600))
			case "public-file":
				require.NoError(t, os.Chmod(filepath.Join(source, AdminBearerName), 0o644))
			case "link":
				require.NoError(t, os.Symlink(filepath.Join(source, AdminBearerName), filepath.Join(source, "link")))
			case "hard-link":
				require.NoError(t, os.Link(filepath.Join(source, AdminBearerName), filepath.Join(source, "link")))
			case "nested":
				destination = filepath.Join(source, "nested")
			case "parent-link":
				link := filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(filepath.Dir(source), link))
				source = filepath.Join(link, LegacyInstallationName)
				destination = filepath.Join(link, InstallationName)
			}
			_, err := InspectRelocation(source, destination)
			require.Error(t, err)
			require.FileExists(t, filepath.Join(source, DatabaseName))
		})
	}
}

func TestRelocationExplicitResumeRetainsPreparedReservation(t *testing.T) {
	source, destination := relocationFixture(t)
	info, err := os.Stat(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(destination, []byte(relocationRecord(source, destination, info)), 0o600))
	_, err = Acquire(destination)
	require.ErrorIs(t, err, ErrUnsafePath)
	relocation, err := InspectRelocation(source, destination)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relocation.Close()) })
	require.NoError(t, relocation.Commit())
	require.True(t, RelocationCompleted(source, destination))
	require.Error(t, relocation.Commit(), "never automatically exchange a completed migration back")
}

func TestRelocationRefusesDestinationChangesAfterPreflight(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			source, destination := relocationFixture(t)
			if prepared {
				info, err := os.Stat(source)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(destination, []byte(relocationRecord(source, destination, info)), 0o600))
			}
			relocation, err := InspectRelocation(source, destination)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, relocation.Close()) })
			require.NoError(t, os.WriteFile(destination, []byte("unknown destination"), 0o600))
			require.Error(t, relocation.Commit())
			require.DirExists(t, source)
			data, err := os.ReadFile(destination)
			require.NoError(t, err)
			require.Equal(t, "unknown destination", string(data))
		})
	}
}

func TestRelocationRefusesNonSiblingFilesystemSelection(t *testing.T) {
	source, _ := relocationFixture(t)
	destination := filepath.Join(t.TempDir(), "different-parent")
	_, err := InspectRelocation(source, destination)
	require.ErrorContains(t, err, "distinct sibling")
	require.DirExists(t, source)
	_, err = os.Lstat(destination)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDefaultSelectionMigrationMatrix(t *testing.T) {
	for _, mode := range []string{"legacy-only", "canonical-only", "neither", "both", "legacy-file", "legacy-link", "custom"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			legacy := filepath.Join(parent, LegacyInstallationName)
			canonical := filepath.Join(parent, InstallationName)
			switch mode {
			case "legacy-only", "both", "custom":
				require.NoError(t, os.Mkdir(legacy, 0o700))
			case "legacy-file":
				require.NoError(t, os.WriteFile(legacy, []byte("unknown"), 0o600))
			case "legacy-link":
				require.NoError(t, os.Symlink(filepath.Join(parent, "missing"), legacy))
			}
			if mode == "canonical-only" || mode == "both" {
				require.NoError(t, os.Mkdir(canonical, 0o700))
			}
			explicit := ""
			if mode == "custom" {
				explicit = legacy
			}
			layout, err := resolveInstallation(explicit, parent, nil)
			if mode == "canonical-only" || mode == "neither" || mode == "custom" {
				require.NoError(t, err)
				if explicit != "" {
					require.Equal(t, explicit, layout.Root)
				} else {
					require.Equal(t, canonical, layout.Root)
				}
			} else {
				require.ErrorIs(t, err, ErrMigrationRequired)
			}
		})
	}
}
