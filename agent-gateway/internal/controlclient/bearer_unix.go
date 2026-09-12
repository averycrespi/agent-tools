//go:build darwin || linux

package controlclient

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func readBearerFile(path string, expectedUID int) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", classifyBearerFileError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", bearerSourceError(ErrBearerSymlink)
	}
	if err := validateBearerFileInfo(info, expectedUID); err != nil {
		return "", err
	}

	descriptor, err := unix.Open(path, unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDONLY, 0)
	if err != nil {
		return "", classifyBearerFileError(err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return "", bearerSourceError(ErrBearerUnreadable)
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return "", bearerSourceError(ErrBearerUnreadable)
	}
	if err := validateBearerFileInfo(info, expectedUID); err != nil {
		return "", err
	}
	contents, err := readBoundedBearer(file)
	if err != nil {
		return "", err
	}
	return validateBearerBytes(contents)
}

func validateBearerFileInfo(info os.FileInfo, expectedUID int) error {
	if !info.Mode().IsRegular() {
		return bearerSourceError(ErrBearerNotRegular)
	}
	permissions := info.Mode().Perm()
	if permissions&0o400 == 0 {
		return bearerSourceError(ErrBearerUnreadable)
	}
	if permissions != 0o400 && permissions != 0o600 {
		return bearerSourceError(ErrBearerPermissions)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != expectedUID {
		return bearerSourceError(ErrBearerOwner)
	}
	return nil
}

func classifyBearerFileError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, unix.ENOENT):
		return bearerSourceError(ErrBearerMissing)
	case errors.Is(err, unix.ELOOP):
		return bearerSourceError(ErrBearerSymlink)
	default:
		return bearerSourceError(ErrBearerUnreadable)
	}
}
