//go:build darwin || linux

package controlclient

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func readBearerFile(path string, expectedUID int) (string, error) {
	descriptor, err := unix.Open(path, unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return "", ErrBearerSource
		}
		return "", ErrBearerSource
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return "", ErrBearerSource
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&^os.FileMode(0o600) != 0 {
		return "", ErrBearerSource
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID {
		return "", ErrBearerSource
	}
	contents, err := readBoundedBearer(file)
	if err != nil {
		return "", err
	}
	return validateBearerBytes(contents)
}
