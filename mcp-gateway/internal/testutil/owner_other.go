//go:build !darwin && !linux

package testutil

import (
	"fmt"
	"os"
)

func ownedByCurrentUser(os.FileInfo) (bool, error) {
	return false, fmt.Errorf("data root ownership validation is unsupported on this platform")
}
