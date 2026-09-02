//go:build !darwin && !linux

package paths

import (
	"fmt"
	"os"
)

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
