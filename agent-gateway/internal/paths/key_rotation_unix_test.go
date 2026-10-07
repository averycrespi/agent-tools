//go:build darwin || linux

package paths

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func rotationPathFixture(t *testing.T) (*Ownership, KeyRotationMaterial) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	owner, err := AcquireForMaintenance(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	material := KeyRotationMaterial{InstallationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", OldKey: bytes.Repeat([]byte{1}, 32), NewKey: bytes.Repeat([]byte{2}, 32)}
	_, err = MasterKey(owner, material.OldKey)
	require.NoError(t, err)
	return owner, material
}

func TestRotationPreparationDurableBoundaries(t *testing.T) {
	for _, point := range []string{"installation_write", "installation_sync", "old-key_write", "old-key_sync", "new-key_write", "new-key_sync", "bundle_sync", "arm_rename", "arm_sync"} {
		t.Run(point, func(t *testing.T) {
			owner, material := rotationPathFixture(t)
			injected := errors.New("interrupt")
			err := prepareKeyRotation(owner, material, func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			current, err := MasterKey(owner, nil)
			require.NoError(t, err)
			require.Equal(t, material.OldKey, current)
			if point == "arm_rename" || point == "arm_sync" {
				require.ErrorIs(t, RequireNoKeyRotation(owner), ErrKeyRotationPending)
				observed, err := ReadKeyRotation(owner)
				require.NoError(t, err)
				require.Equal(t, material, observed)
				name, err := RetainKeyRotation(owner, observed)
				require.NoError(t, err)
				old, err := ReadRecoveryKey(filepath.Join(owner.Layout().Root, name, "old-key"))
				require.NoError(t, err)
				require.Equal(t, material.OldKey, old)
			} else {
				require.NoError(t, RequireNoKeyRotation(owner))
			}
		})
	}
}

func TestRotationKeyPublicationDurableBoundaries(t *testing.T) {
	for _, point := range []string{"key_write", "key_sync", "key_stage_sync", "key_rename", "key_directory_sync"} {
		t.Run(point, func(t *testing.T) {
			owner, material := rotationPathFixture(t)
			require.NoError(t, PrepareKeyRotation(owner, material))
			injected := errors.New("interrupt")
			err := publishRotatedKey(owner, material, func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			require.ErrorIs(t, RequireNoKeyRotation(owner), ErrKeyRotationPending)
			observed, err := ReadKeyRotation(owner)
			require.NoError(t, err)
			require.Equal(t, material, observed)
			current, err := MasterKey(owner, nil)
			require.NoError(t, err)
			if point == "key_rename" || point == "key_directory_sync" {
				require.Equal(t, material.NewKey, current)
			} else {
				require.Equal(t, material.OldKey, current)
			}
			require.NoError(t, PublishRotatedKey(owner, material))
			current, err = MasterKey(owner, nil)
			require.NoError(t, err)
			require.Equal(t, material.NewKey, current)
		})
	}
}

func TestRotationRetentionDurableBoundaries(t *testing.T) {
	for _, point := range []string{"retain_rename", "retain_sync"} {
		t.Run(point, func(t *testing.T) {
			owner, material := rotationPathFixture(t)
			require.NoError(t, PrepareKeyRotation(owner, material))
			require.NoError(t, PublishRotatedKey(owner, material))
			injected := errors.New("interrupt")
			_, err := retainKeyRotation(owner, material, func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			require.NoError(t, RequireNoKeyRotation(owner))
			matches, err := filepath.Glob(filepath.Join(owner.Layout().Root, "master-key-retained-*"))
			require.NoError(t, err)
			require.Len(t, matches, 1)
			for name, want := range map[string][]byte{"old-key": material.OldKey, "new-key": material.NewKey} {
				got, err := ReadRecoveryKey(filepath.Join(matches[0], name))
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
		})
	}
}

func TestRecoveryKeyRejectsUnsafeMaterial(t *testing.T) {
	for _, mode := range []string{"symlink", "hardlink", "mode", "short", "long", "directory"} {
		t.Run(mode, func(t *testing.T) {
			owner, _ := rotationPathFixture(t)
			source := filepath.Join(owner.Layout().Root, MasterKeyName)
			path := filepath.Join(owner.Layout().Root, "input")
			switch mode {
			case "symlink":
				require.NoError(t, os.Symlink(source, path))
			case "hardlink":
				require.NoError(t, os.Link(source, path))
			case "mode":
				require.NoError(t, os.WriteFile(path, make([]byte, 32), 0644))
			case "short":
				require.NoError(t, os.WriteFile(path, make([]byte, 31), 0600))
			case "long":
				require.NoError(t, os.WriteFile(path, make([]byte, 33), 0600))
			case "directory":
				require.NoError(t, os.Mkdir(path, 0700))
			}
			value, err := ReadRecoveryKey(path)
			require.Error(t, err)
			require.Empty(t, value)
		})
	}
}
