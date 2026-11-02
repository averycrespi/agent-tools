//go:build darwin || linux

package paths

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func initializationRootIdentity(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if err := validateOwnerOnlyDirectory(info, root); err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrUnsafePath
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func openInitializationFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		err = validateOwnerOnlyFile(info, path)
	}
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			err = ErrUnsafePath
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
