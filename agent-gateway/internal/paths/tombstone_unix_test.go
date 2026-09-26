//go:build darwin || linux

package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSelectionLegacyMatrix(t *testing.T) {
	for _, mode := range []string{"legacy-only", "canonical-only", "neither", "both", "legacy-file", "legacy-link", "explicit-legacy", "custom"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			legacy := filepath.Join(parent, LegacyInstallationName)
			canonical := filepath.Join(parent, InstallationName)
			switch mode {
			case "legacy-only", "both", "explicit-legacy", "custom":
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
			switch mode {
			case "explicit-legacy":
				explicit = legacy
			case "custom":
				explicit = filepath.Join(parent, "custom")
				require.NoError(t, os.Mkdir(explicit, 0o700))
			}
			before, err := os.ReadDir(parent)
			require.NoError(t, err)
			layout, err := resolveInstallation(explicit, parent, nil)
			require.NoError(t, err)
			if explicit != "" {
				require.Equal(t, explicit, layout.Root)
			} else {
				require.Equal(t, canonical, layout.Root)
			}
			after, err := os.ReadDir(parent)
			require.NoError(t, err)
			require.Equal(t, before, after, "resolution must not create or remove entries")
		})
	}
}

func TestCompletedTombstoneSelection(t *testing.T) {
	for _, mode := range []string{"completed", "wrong-inode", "wrong-device", "wrong-source", "wrong-destination", "wrong-version", "incomplete", "oversized", "public-tombstone", "tombstone-link", "public-root", "root-link", "missing-root", "reservation"} {
		t.Run(mode, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			legacy := filepath.Join(parent, LegacyInstallationName)
			canonical := filepath.Join(parent, InstallationName)
			require.NoError(t, os.Mkdir(canonical, 0o700))
			info, err := os.Lstat(canonical)
			require.NoError(t, err)
			stat := info.Sys().(*syscall.Stat_t)
			// Historical wire format, not a call to the production recognizer or
			// the retired writer: compatibility must not drift with implementation.
			device, inode := fmt.Sprint(stat.Dev), fmt.Sprint(stat.Ino)
			source, destination, magic := legacy, canonical, "agent-gateway relocation v1"
			switch mode {
			case "wrong-inode":
				inode = "0"
			case "wrong-device":
				device = "invalid"
			case "wrong-source":
				source += "-other"
			case "wrong-destination":
				destination += "-other"
			case "wrong-version":
				magic = "agent-gateway relocation v2"
			}
			record := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", magic, source, destination, device, inode)
			switch mode {
			case "incomplete":
				record = strings.TrimSuffix(record, "\n")
			case "oversized":
				record += strings.Repeat("x", 8193)
			}
			require.NoError(t, os.WriteFile(legacy, []byte(record), 0o600))
			switch mode {
			case "public-tombstone":
				require.NoError(t, os.Chmod(legacy, 0o644))
			case "tombstone-link":
				require.NoError(t, os.Rename(legacy, legacy+"-record"))
				require.NoError(t, os.Symlink(legacy+"-record", legacy))
			case "public-root":
				require.NoError(t, os.Chmod(canonical, 0o755))
			case "root-link":
				require.NoError(t, os.Rename(canonical, canonical+"-original"))
				require.NoError(t, os.Symlink(canonical+"-original", canonical))
			case "missing-root":
				require.NoError(t, os.Remove(canonical))
			case "reservation":
				require.NoError(t, os.Rename(legacy, legacy+"-record"))
				require.NoError(t, os.Rename(canonical, legacy))
				require.NoError(t, os.Rename(legacy+"-record", canonical))
			}
			layout, err := resolveInstallation("", parent, nil)
			require.NoError(t, err)
			require.Equal(t, canonical, layout.Root)
			// Historical recognition no longer controls implicit selection.
			require.Equal(t, mode == "completed", relocationCompleted(legacy, canonical))
			if mode == "completed" {
				_, err = Prepare(legacy)
				require.ErrorIs(t, err, ErrUnsafePath, "selected-root safety still rejects a tombstone")
			}
			recordPath := legacy
			if mode == "reservation" {
				recordPath = canonical
			}
			retained, err := os.ReadFile(recordPath)
			require.NoError(t, err)
			require.Equal(t, record, string(retained), "selection must retain historical evidence")
		})
	}
}
