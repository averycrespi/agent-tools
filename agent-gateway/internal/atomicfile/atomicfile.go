// Package atomicfile writes files using a temp-file + rename pattern so that
// readers never observe a partially-written file and a crash mid-write never
// leaves the target truncated.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path with the given permissions, atomically.
//
// Steps: create a temp file in the same directory, chmod, write, fsync, close,
// rename over the destination, then fsync the parent directory so the rename
// survives a crash. The temp file is removed on any failure before rename.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	f, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp: %w", err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()

	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomicfile: chmod temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomicfile: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomicfile: fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	committed = true

	// Best-effort fsync of the parent directory so the rename is durable.
	// Some filesystems (notably on Windows) do not support directory fsync;
	// silently ignore EINVAL/ENOTSUP-class failures by not surfacing them.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
