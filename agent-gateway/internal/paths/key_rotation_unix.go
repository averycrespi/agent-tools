//go:build darwin || linux

package paths

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ReadRecoveryKey reads an explicitly selected, separately safeguarded key.
// It is never a provider fallback and never creates or repairs a file.
func ReadRecoveryKey(path string) ([]byte, error) {
	value, err := readRotationFile(path, 32)
	if err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

func readRotationFile(path string, size int64) ([]byte, error) {
	if err := InspectRoot(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err = validateOwnerOnlyFile(info, path); err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, ErrUnsafePath
	}
	value, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil || int64(len(value)) != size {
		clear(value)
		return nil, errors.Join(ErrUnsafePath, err)
	}
	return value, nil
}

func rotationStep(fault func(string) error, point string) error {
	if fault != nil {
		return fault(point)
	}
	return nil
}

func writeRotationFile(path string, value []byte, fault func(string) error, label string) error {
	file, err := CreateOwnerOnlyFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	n, err := file.Write(value)
	if err != nil {
		return err
	}
	if n != len(value) {
		return io.ErrShortWrite
	}
	if err = rotationStep(fault, label+"_write"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = rotationStep(fault, label+"_sync"); err != nil {
		return err
	}
	return file.Close()
}

// PrepareKeyRotation durably retains both keys before atomically arming the
// fixed recovery slot. Incomplete preparation directories are retained, never
// adopted or reused. No database encryption may precede successful return.
func PrepareKeyRotation(owner *Ownership, material KeyRotationMaterial) error {
	return prepareKeyRotation(owner, material, nil)
}

func prepareKeyRotation(owner *Ownership, material KeyRotationMaterial, fault func(string) error) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	if err = RequireNoKeyRotation(owner); err != nil {
		return err
	}
	if len(material.InstallationID) != 26 || len(material.OldKey) != 32 || len(material.NewKey) != 32 || bytes.Equal(material.OldKey, material.NewKey) {
		return ErrUnsafePath
	}
	current, err := DurableMasterKey(owner)
	if err != nil {
		return err
	}
	defer clear(current)
	if !bytes.Equal(current, material.OldKey) {
		return ErrUnsafePath
	}
	stage, err := os.MkdirTemp(layout.Root, ".master-key-preparation-")
	if err != nil {
		return err
	}
	for _, part := range []struct {
		name  string
		value []byte
	}{
		{"installation", []byte(material.InstallationID)}, {"old-key", material.OldKey}, {"new-key", material.NewKey},
	} {
		if err = writeRotationFile(filepath.Join(stage, part.name), part.value, fault, part.name); err != nil {
			return err
		}
	}
	if err = syncDirectory(stage); err != nil {
		return err
	}
	if err = rotationStep(fault, "bundle_sync"); err != nil {
		return err
	}
	if err = os.Rename(stage, filepath.Join(layout.Root, KeyRotationName)); err != nil {
		return err
	}
	if err = rotationStep(fault, "arm_rename"); err != nil {
		return err
	}
	if err = syncDirectory(layout.Root); err != nil {
		return err
	}
	return rotationStep(fault, "arm_sync")
}

func ReadKeyRotation(owner *Ownership) (material KeyRotationMaterial, err error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return material, err
	}
	defer func() {
		if err != nil {
			material.Clear()
		}
	}()
	root := filepath.Join(layout.Root, KeyRotationName)
	installation, err := readRotationFile(filepath.Join(root, "installation"), 26)
	if err != nil {
		return material, err
	}
	material.InstallationID = string(installation)
	material.OldKey, err = ReadRecoveryKey(filepath.Join(root, "old-key"))
	if err != nil {
		return material, err
	}
	material.NewKey, err = ReadRecoveryKey(filepath.Join(root, "new-key"))
	if err == nil && bytes.Equal(material.OldKey, material.NewKey) {
		err = ErrUnsafePath
	}
	return material, err
}

// PublishRotatedKey replaces only the observed old key, retaining both recovery
// copies. A matching new key is sealed again during explicit reconciliation.
func PublishRotatedKey(owner *Ownership, material KeyRotationMaterial) error {
	return publishRotatedKey(owner, material, nil)
}

func publishRotatedKey(owner *Ownership, material KeyRotationMaterial, fault func(string) error) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	current, err := DurableMasterKey(owner)
	if err != nil {
		return err
	}
	defer clear(current)
	if bytes.Equal(current, material.NewKey) {
		return nil
	}
	if !bytes.Equal(current, material.OldKey) {
		return ErrUnsafePath
	}
	// Create a private unique directory so failed/partial key publication is
	// retained separately and can never overwrite either recovery copy.
	stage, err := os.MkdirTemp(layout.Root, ".master-key-publication-")
	if err != nil {
		return err
	}
	path := filepath.Join(stage, "key")
	if err = writeRotationFile(path, material.NewKey, fault, "key"); err != nil {
		return err
	}
	if err = syncDirectory(stage); err != nil {
		return err
	}
	if err = rotationStep(fault, "key_stage_sync"); err != nil {
		return err
	}
	if err = os.Rename(path, filepath.Join(layout.Root, MasterKeyName)); err != nil {
		return err
	}
	if err = rotationStep(fault, "key_rename"); err != nil {
		return err
	}
	if err = syncDirectory(layout.Root); err != nil {
		return err
	}
	return rotationStep(fault, "key_directory_sync")
}

// RetainKeyRotation removes the startup fence by durable rename, not deletion.
// Even aborted candidates are retained and must never be reused for encryption.
func RetainKeyRotation(owner *Ownership, material KeyRotationMaterial) (string, error) {
	return retainKeyRotation(owner, material, nil)
}

func retainKeyRotation(owner *Ownership, material KeyRotationMaterial, fault func(string) error) (string, error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(material.NewKey)
	name := "master-key-retained-" + hex.EncodeToString(digest[:])
	target := filepath.Join(layout.Root, name)
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", ErrUnsafePath
	}
	if err = os.Rename(filepath.Join(layout.Root, KeyRotationName), target); err != nil {
		return "", err
	}
	if err = rotationStep(fault, "retain_rename"); err != nil {
		return "", err
	}
	if err = syncDirectory(layout.Root); err != nil {
		return "", err
	}
	if err = rotationStep(fault, "retain_sync"); err != nil {
		return "", err
	}
	return name, nil
}
