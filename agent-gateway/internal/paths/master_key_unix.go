//go:build darwin || linux

package paths

import (
	"errors"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const MasterKeyName = "master-key"

// MasterKey reads or exclusively creates the fixed installation key. A failed
// creation retains the file, including partial bytes: setup never replaces it.
func MasterKey(owner *Ownership, create []byte) ([]byte, error) {
	return masterKey(owner, create, false)
}

// DurableMasterKey seals an existing complete key before stopped setup binds it.
// This covers interruption after file publication but before directory sync.
func DurableMasterKey(owner *Ownership) ([]byte, error) {
	return masterKey(owner, nil, true)
}

func masterKey(owner *Ownership, create []byte, durable bool) ([]byte, error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return nil, err
	}
	if err = InspectRoot(layout.Root); err != nil {
		return nil, err
	}
	dir, err := unix.Open(layout.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dir) }()
	var stat unix.Stat_t
	if err = unix.Fstat(dir, &stat); err != nil {
		return nil, err
	}
	if int64(stat.Uid) != int64(os.Geteuid()) || stat.Mode&0o7777 != 0o700 {
		return nil, ErrUnsafePath
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if create != nil {
		if len(create) != 32 {
			return nil, ErrUnsafePath
		}
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC
	}
	fd, err := unix.Openat(dir, MasterKeyName, flags, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), MasterKeyName)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err = validateOwnerOnlyFile(info, MasterKeyName); err != nil {
		return nil, err
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, ErrUnsafePath
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Nlink != 1 {
		return nil, ErrUnsafePath
	}
	if create != nil {
		n, writeErr := file.Write(create)
		if writeErr != nil {
			return nil, writeErr
		}
		if n != len(create) {
			return nil, io.ErrShortWrite
		}
		if err = file.Sync(); err != nil {
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		if err = unix.Fsync(dir); err != nil {
			return nil, err
		}
		return append([]byte(nil), create...), nil
	}
	value, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(value) != 32 {
		clear(value)
		return nil, errors.New("installation master key is incomplete")
	}
	if durable {
		if err = file.Sync(); err == nil {
			err = unix.Fsync(dir)
		}
		if err != nil {
			clear(value)
			return nil, err
		}
	}
	return value, nil
}
