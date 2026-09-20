//go:build darwin || linux

package paths

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// relocationCompleted recognizes the persisted v1 tombstone from the retired
// migrator. Only the original directory inode qualifies, never a copied root.
// The regular file at the legacy path also blocks older binaries' initialization.
func relocationCompleted(source, destination string) bool {
	info, err := os.Lstat(destination)
	if err != nil || validateOwnerOnlyDirectory(info, destination) != nil || ValidateOwnerOnlyFile(source) != nil {
		return false
	}
	file, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	stat := info.Sys().(*syscall.Stat_t)
	expected := strings.Join([]string{"agent-gateway relocation v1", source, destination, fmt.Sprint(stat.Dev), strconv.FormatUint(stat.Ino, 10), ""}, "\n")
	return err == nil && len(data) <= 8192 && string(data) == expected
}
