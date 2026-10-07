package paths

import (
	"errors"
	"os"
	"path/filepath"
)

const KeyRotationName = "master-key.rotation"

var ErrKeyRotationPending = errors.New("master-key rotation recovery is required; preserve all keys and database files")

// RequireNoKeyRotation fences ordinary writers before they open storage. The
// existence of any object in the reserved slot is evidence, not an invitation
// to repair it or retry the rotation.
func RequireNoKeyRotation(owner *Ownership) error {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return err
	}
	_, err = os.Lstat(filepath.Join(layout.Root, KeyRotationName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(ErrKeyRotationPending, err)
}

// KeyRotationMaterial is borrowed only by stopped rotation/recovery. Callers
// clear both slices as soon as the operation settles; neither may be logged.
type KeyRotationMaterial struct {
	InstallationID string
	OldKey         []byte
	NewKey         []byte
}

func (material *KeyRotationMaterial) Clear() { clear(material.OldKey); clear(material.NewKey) }
