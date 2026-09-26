//go:build !darwin && !linux

package paths

import (
	"fmt"
	"os"
)

func relocationCompleted(string, string) bool { return false }

func CheckCertificateDestination(string, []byte) error {
	return fmt.Errorf("certificate inspection is unsupported on this platform")
}

func PublishCertificate(string, []byte, []byte) error {
	return fmt.Errorf("certificate publication is unsupported on this platform")
}

func ProbeOwnership(string) (bool, error) {
	return false, fmt.Errorf("ownership inspection is unsupported on this platform")
}

func ReserveHeadroom(string, int64) (func(), error) {
	return nil, fmt.Errorf("storage headroom validation is unsupported on this platform")
}

func acquireExistingOwnership(string, bool) (*Ownership, error) {
	return nil, fmt.Errorf("installation locking is unsupported on this platform")
}

func AcquireStoppedExisting(string) (*Ownership, error) {
	return nil, fmt.Errorf("stopped storage migration is unsupported on this platform")
}

func validateOwner(os.FileInfo) error {
	return fmt.Errorf("installation ownership validation is unsupported on this platform")
}

func acquireFileLock(string) (*os.File, error) {
	return nil, fmt.Errorf("installation locking is unsupported on this platform")
}

func releaseFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func createOwnerOnlyFile(string) (*os.File, error) {
	return nil, fmt.Errorf("owner-only file creation is unsupported on this platform")
}
