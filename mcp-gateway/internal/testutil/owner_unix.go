//go:build darwin || linux

package testutil

import (
	"fmt"
	"os"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("data root ownership metadata is unavailable")
	}
	return int64(stat.Uid) == int64(os.Geteuid()), nil
}
