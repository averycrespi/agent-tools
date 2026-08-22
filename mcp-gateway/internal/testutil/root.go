package testutil

import (
	"fmt"
	"os"
	"testing"
)

func NewOwnerOnlyDataRoot(testingTB testing.TB) string {
	testingTB.Helper()

	root := testingTB.TempDir()
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // Owner-only directories require execute permission.
		testingTB.Fatalf("set owner-only data-root permissions: %v", err)
	}
	if err := ValidateOwnerOnlyDataRoot(root); err != nil {
		testingTB.Fatalf("validate owner-only data root: %v", err)
	}
	return root
}

func ValidateOwnerOnlyDataRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect data root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data root is a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("data root is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("data root permissions are %04o, want 0700", info.Mode().Perm())
	}
	owned, err := ownedByCurrentUser(info)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("data root is not owned by the current user")
	}
	return nil
}
