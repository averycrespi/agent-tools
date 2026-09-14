//go:build darwin || linux

package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func inspect(path string, uid int, dir, exact bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || info.Mode()&os.ModeSymlink != 0 || (dir && !info.IsDir()) || (!dir && !info.Mode().IsRegular()) {
		return fmt.Errorf("unsafe type or ownership: %s", path)
	}
	wanted := os.FileMode(0600)
	if dir {
		wanted = 0700
	}
	if info.Mode().Perm()&0022 != 0 || exact && info.Mode().Perm() != wanted || !dir && stat.Nlink != 1 {
		return fmt.Errorf("unsafe permissions or hard links: %s", path)
	}
	return nil
}
func noSymlinks(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinked path: %s", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}
func (m *manager) directories() []string {
	return []string{m.home, filepath.Join(m.home, "Library"), filepath.Join(m.home, "Library", "LaunchAgents"), filepath.Join(m.home, "Library", "Logs"), m.logs()}
}
func (m *manager) logs() string { return filepath.Join(m.home, "Library", "Logs", "agent-gateway") }
func (m *manager) plist() string {
	return filepath.Join(m.home, "Library", "LaunchAgents", Label+".plist")
}
func (m *manager) preflight(d definition, requireLogs bool) error {
	for _, dir := range m.directories() {
		if err := noSymlinks(dir); err != nil {
			return err
		}
		err := inspect(dir, m.uid, true, dir == m.logs())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if d.Stdout != filepath.Join(m.logs(), "stdout.log") || d.Stderr != filepath.Join(m.logs(), "stderr.log") {
		return errors.New("unsupported log destinations; restore canonical private log paths explicitly")
	}
	for _, p := range []string{d.Stdout, d.Stderr} {
		err := inspect(p, m.uid, false, true)
		if err != nil && (requireLogs || !errors.Is(err, os.ErrNotExist)) {
			return err
		}
	}
	if err := noSymlinks(d.DataDir); err != nil {
		return err
	}
	if err := inspect(d.DataDir, m.uid, true, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := noSymlinks(d.Binary); err != nil {
		return err
	}
	if err := inspect(d.Binary, m.uid, false, false); err != nil {
		return err
	}
	info, err := os.Stat(d.Binary)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0100 == 0 {
		return errors.New("selected binary is not executable; install it separately")
	}
	return d.validate()
}
func readPrivate(path string, uid int) ([]byte, os.FileInfo, error) {
	if err := inspect(path, uid, false, true); err != nil {
		return nil, nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return nil, nil, errors.New("file identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, definitionLimit+1))
	if len(data) > definitionLimit {
		return nil, nil, errors.New("file exceeds bound")
	}
	return data, info, err
}
func (m *manager) read() (definition, []byte, os.FileInfo, error) {
	if err := noSymlinks(m.plist()); err != nil {
		return definition{}, nil, nil, err
	}
	for _, directory := range m.directories()[:3] {
		if err := inspect(directory, m.uid, true, false); err != nil {
			return definition{}, nil, nil, err
		}
	}
	data, info, err := readPrivate(m.plist(), m.uid)
	if err != nil {
		return definition{}, nil, nil, err
	}
	d, err := decode(data)
	if err == nil && (d.Stdout != filepath.Join(m.logs(), "stdout.log") || d.Stderr != filepath.Join(m.logs(), "stderr.log")) {
		err = errors.New("unsupported log destinations; reconcile the canonical private plist")
	}
	return d, data, info, err
}
func unchanged(path string, uid int, data []byte, info os.FileInfo) error {
	now, current, err := readPrivate(path, uid)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || !bytes.Equal(data, now) {
		return errors.New("installed plist changed during management; inspect before another operation")
	}
	return nil
}
func (m *manager) lock() (func(), error) {
	path := filepath.Join(filepath.Dir(m.plist()), "."+Label+".lock")
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	if err := inspect(path, m.uid, false, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = inspect(path, m.uid, false, true); err != nil {
		_ = f.Close()
		return nil, err
	}
	info, e := f.Stat()
	current, e2 := os.Lstat(path)
	if e != nil || e2 != nil || !os.SameFile(info, current) {
		_ = f.Close()
		return nil, errors.New("management lock identity changed")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another canonical service mutation is active")
	}
	// Never unlink the lock: another process may already have this inode open.
	return func() { _ = f.Close() }, nil
}
func (m *manager) prepareDirectories() error {
	for _, dir := range m.directories()[1:] {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := inspect(dir, m.uid, true, dir == m.logs()); err != nil {
			return err
		}
	}
	return nil
}
func (m *manager) prepareLogs(d definition) error {
	for _, path := range []string{d.Stdout, d.Stderr} {
		if err := inspect(path, m.uid, false, true); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
	}
	return nil
}
func publish(path string, data []byte, replace bool) (bool, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+Label+"-*")
	if err != nil {
		return false, err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	_, err = temp.Write(data)
	err = errors.Join(err, temp.Sync(), temp.Close())
	if err != nil {
		return false, err
	}
	if replace {
		err = os.Rename(name, path)
	} else {
		err = os.Link(name, path)
	}
	if err != nil {
		return false, err
	}
	if !replace {
		if err = os.Remove(name); err != nil {
			return true, err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, err
	}
	return true, errors.Join(dir.Sync(), dir.Close())
}
