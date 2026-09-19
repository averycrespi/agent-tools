//go:build !darwin && !linux

package paths

import (
	"fmt"
	"os"
)

func RelocationCompleted(string, string) bool { return false }

func ReserveHeadroom(string, int64) (func(), error) {
	return nil, fmt.Errorf("storage headroom validation is unsupported on this platform")
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
