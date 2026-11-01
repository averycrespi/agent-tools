package testutil

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RetiredBackupArtifacts creates only disposable historical inventory and restore
// residue. Shared mutation/initialization/rotation markers are deliberately absent.
func RetiredBackupArtifacts(t testing.TB, root string) []string {
	t.Helper()
	inventory := filepath.Join(root, "backups")
	require.NoError(t, os.Mkdir(inventory, 0o755)) //nolint:gosec // Deliberately unsafe mode in a disposable historical-artifact fixture.
	published := filepath.Join(inventory, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, os.Mkdir(published, 0o755))                                                                               //nolint:gosec // Deliberately unsafe mode; removal must neither validate nor repair it.
	require.NoError(t, os.WriteFile(filepath.Join(published, "metadata.json"), []byte("{malformed historical metadata"), 0o644)) //nolint:gosec // Deliberately malformed and unsafe disposable inventory.
	require.NoError(t, os.WriteFile(filepath.Join(published, "gateway.db"), []byte("historical opaque database"), 0o644))        //nolint:gosec // Opaque disposable artifact, not live storage.
	require.NoError(t, os.WriteFile(filepath.Join(inventory, ".interrupted-stage"), []byte("retain"), 0o644))                    //nolint:gosec // Deliberately unsafe retained staging fixture.
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("external sentinel"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(inventory, "linked-artifact")))
	paths := []string{inventory, outside}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".mutation", ".mutation.tmp", ".mutation.cleared"} {
		path := filepath.Join(root, "gateway.db.restore"+suffix)
		require.NoError(t, os.WriteFile(path, []byte("retained restore residue "+suffix), 0o644)) //nolint:gosec // Deliberately unsafe historical residue in a disposable installation.
		paths = append(paths, path)
	}
	return paths
}

type ArtifactFingerprint struct {
	Mode     fs.FileMode
	Size     int64
	Modified time.Time
	Digest   [32]byte
	Link     string
}

// FingerprintArtifacts never follows links. Include directory metadata so an
// empty inventory recreation, chmod, rename, or partial cleanup is observable.
func FingerprintArtifacts(t testing.TB, paths []string) map[string]ArtifactFingerprint {
	t.Helper()
	result := map[string]ArtifactFingerprint{}
	for _, root := range paths {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			value := ArtifactFingerprint{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime()}
			if info.Mode()&os.ModeSymlink != 0 {
				value.Link, err = os.Readlink(path)
			} else if info.Mode().IsRegular() {
				var data []byte
				data, err = os.ReadFile(path) //nolint:gosec // Test-owned quiescent trees; links are fingerprinted separately and never traversed.
				value.Digest = sha256.Sum256(data)
			}
			if err != nil {
				return err
			}
			result[path] = value
			return nil
		}))
	}
	return result
}
