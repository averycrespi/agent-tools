//go:build !darwin && !linux

package paths

func ReadRecoveryKey(string) ([]byte, error)                   { return nil, ErrUnsafePath }
func PrepareKeyRotation(*Ownership, KeyRotationMaterial) error { return ErrUnsafePath }
func ReadKeyRotation(*Ownership) (KeyRotationMaterial, error) {
	return KeyRotationMaterial{}, ErrUnsafePath
}
func PublishRotatedKey(*Ownership, KeyRotationMaterial) error           { return ErrUnsafePath }
func RetainKeyRotation(*Ownership, KeyRotationMaterial) (string, error) { return "", ErrUnsafePath }
