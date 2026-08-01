// Package atomicfile writes a file in one step, so a reader never observes a
// partially written result.
//
// Ported from the agent-gateway branch of this repository
// (origin/agent-gateway, internal/atomicfile), unchanged apart from creating
// the parent directory.
//
// The CA needs this: `ca rotate` replaces ca.key and ca.pem while a running
// proxy may be reading them, and a torn key file would break TLS termination
// for every sandbox at once.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path atomically, creating parent directories as
// needed. The file is created with perm; an existing file is replaced.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("atomicfile: create parent directory: %w", err)
	}

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

	// Chmod before writing, so the contents are never briefly readable under
	// the default 0600-from-CreateTemp on a path that wants tighter bits, nor
	// world-readable before the intended mode lands.
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

	// Best-effort fsync of the parent directory so the rename itself is
	// durable. Not every filesystem supports directory fsync, so a failure
	// here is not surfaced.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
