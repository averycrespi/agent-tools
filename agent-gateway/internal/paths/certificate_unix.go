//go:build darwin || linux

package paths

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// CheckCertificateDestination validates a future managed publication without
// creating, truncating, or repairing anything. Publication repeats these checks.
func CheckCertificateDestination(path string, expected []byte) error {
	if err := ValidateOutputDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateOwnerOnlyFile(info, path); err != nil {
		return err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return ErrUnsafePath
	}
	value, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || len(expected) == 0 || !bytes.Equal(value, expected) {
		return ErrUnsafePath
	}
	return nil
}

// PublishCertificate creates a private public-certificate file or leaves an
// identical one alone. Only the managed destination may supply its previously
// selected public bytes for replacement; unrelated output is never overwritten.
// The caller retains installation ownership through publication.
func PublishCertificate(path string, certificate, previous []byte) error {
	if len(certificate) == 0 || len(certificate) > 16384 {
		return ErrUnsafePath
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if err = validateOwnerOnlyDirectory(info, parent); err != nil {
		return err
	}
	existing, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return publishNewCertificate(path, certificate, (*os.File).Write)
	}
	if err != nil {
		return err
	}
	oldInfo, statErr := existing.Stat()
	if statErr != nil {
		_ = existing.Close()
		return statErr
	}
	if err = validateOwnerOnlyFile(oldInfo, path); err != nil {
		_ = existing.Close()
		return err
	}
	if oldInfo.Sys().(*syscall.Stat_t).Nlink != 1 {
		_ = existing.Close()
		return ErrUnsafePath
	}
	old, readErr := io.ReadAll(io.LimitReader(existing, 16385))
	if err = errors.Join(readErr, existing.Close()); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(oldInfo, current) {
		return ErrUnsafePath
	}
	if bytes.Equal(old, certificate) {
		return nil
	}
	if len(previous) == 0 || !bytes.Equal(old, previous) {
		return ErrUnsafePath
	}
	stagePath, err := stageCertificate(parent, certificate, (*os.File).Write)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stagePath) }()
	current, err = os.Lstat(path)
	if err != nil || !os.SameFile(oldInfo, current) {
		return ErrUnsafePath
	}
	if err = os.Rename(stagePath, path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func stageCertificate(parent string, certificate []byte, write func(*os.File, []byte) (int, error)) (string, error) {
	stage, err := os.CreateTemp(parent, ".http-ca-*")
	if err != nil {
		return "", err
	}
	n, writeErr := write(stage, certificate)
	if n != len(certificate) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	syncErr := stage.Sync()
	closeErr := stage.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", errors.Join(err, os.Remove(stage.Name()))
	}
	return stage.Name(), nil
}

func publishNewCertificate(path string, certificate []byte, write func(*os.File, []byte) (int, error)) error {
	parent := filepath.Dir(path)
	stage, err := stageCertificate(parent, certificate, write)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stage) }()
	// Link publishes complete, synced bytes atomically without replacing a file
	// that appeared after preflight. Remove the staging link before directory sync.
	if err := os.Link(stage, path); err != nil {
		return err
	}
	if err := os.Remove(stage); err != nil {
		return err
	}
	return syncDirectory(parent)
}
