//go:build darwin || linux

package paths

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ownership metadata is unavailable")
	}
	return validateOwnerID(int64(stat.Uid), int64(os.Geteuid()))
}

func validateOwnerID(actual, expected int64) error {
	if actual != expected {
		return fmt.Errorf("%w: owner is %d, want %d", ErrUnsafePath, actual, expected)
	}
	return nil
}

func acquireFileLock(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_CLOEXEC|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: lock file is a symlink", ErrUnsafePath)
		}
		return nil, fmt.Errorf("open installation lock: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("open installation lock: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect installation lock: %w", err)
	}
	if err := validateOwnerOnlyFile(info, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync installation lock: %w", err)
	}
	if err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrInUse
		}
		return nil, fmt.Errorf("acquire installation lock: %w", err)
	}
	if err := syncDirectory(filepathDir(path)); err != nil {
		_ = unix.Flock(descriptor, unix.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("sync data root after opening lock: %w", err)
	}
	return file, nil
}

func releaseFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func createOwnerOnlyFile(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_CLOEXEC|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: file is a symlink", ErrUnsafePath)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fmt.Errorf("invalid file descriptor")
	}
	return file, nil
}

func filepathDir(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if os.IsPathSeparator(path[index]) {
			if index == 0 {
				return path[:1]
			}
			return path[:index]
		}
	}
	return "."
}
