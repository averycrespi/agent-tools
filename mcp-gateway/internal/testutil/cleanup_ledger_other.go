//go:build !darwin && !linux

package testutil

import (
	"fmt"
	"syscall"
)

func externalProcessIdentity(int) (string, error) {
	return "", fmt.Errorf("cross-process cleanup identity is unsupported on this platform")
}

func revalidateExternalProcessGroup(int, int, string) bool { return false }

func signalExternalProcessGroup(int, syscall.Signal) error { return syscall.ENOTSUP }
