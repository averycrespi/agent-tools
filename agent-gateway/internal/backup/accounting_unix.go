//go:build darwin || linux

package backup

import (
	"os"

	"golang.org/x/sys/unix"
)

func openAccountingDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	return accountingDescriptor(descriptor, path, true, err)
}

func openAccountingChildDirectory(parent *os.File, name string) (*os.File, error) {
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	return accountingDescriptor(descriptor, name, true, err)
}

func openAccountingFile(parent *os.File, name string) (*os.File, error) {
	// NONBLOCK prevents an unsafe FIFO from blocking before descriptor validation.
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	return accountingDescriptor(descriptor, name, false, err)
}

func accountingDescriptor(descriptor int, name string, directory bool, openErr error) (*os.File, error) {
	if openErr != nil {
		return nil, openErr
	}
	file := os.NewFile(uintptr(descriptor), name)
	info, err := file.Stat()
	if err == nil {
		var stat unix.Stat_t
		err = unix.Fstat(descriptor, &stat)
		expected := os.FileMode(0o600)
		if directory {
			expected = 0o700
		}
		if err == nil && (int64(stat.Uid) != int64(os.Geteuid()) || info.Mode().Perm() != expected || directory && !info.IsDir() || !directory && !info.Mode().IsRegular()) {
			err = ErrInvalidArtifact
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
