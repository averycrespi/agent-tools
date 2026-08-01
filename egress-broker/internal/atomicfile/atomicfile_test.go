package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/egress-broker/internal/atomicfile"
)

func TestWriteCreatesFileWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	if err := atomicfile.Write(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "payload" {
		t.Errorf("contents = %q, want %q", data, "payload")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

func TestWriteReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	if err := atomicfile.Write(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := atomicfile.Write(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "second" {
		t.Errorf("contents = %q, want %q", data, "second")
	}
}

func TestWriteCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "f")

	if err := atomicfile.Write(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}

// TestWriteLeavesNoTempOnSuccess guards against the temp file being left
// behind, which would accumulate half-written key material in the data dir.
func TestWriteLeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	if err := atomicfile.Write(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "f" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only the target file", names)
	}
}

func TestWriteFailsOnUnwritableParent(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// blocker is a file, so it cannot also be a parent directory.
	if err := atomicfile.Write(filepath.Join(blocker, "child"), []byte("x"), 0o600); err == nil {
		t.Error("Write into a path whose parent is a file = nil, want an error")
	}
}
