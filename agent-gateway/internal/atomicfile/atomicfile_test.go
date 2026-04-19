package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFileWithPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")

	if err := Write(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("perm = %o, want 0o600", mode)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("contents = %q, want %q", got, "hello")
	}
}

func TestWriteOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")

	if err := os.WriteFile(path, []byte("old contents"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("contents = %q, want %q", got, "new")
	}
	info, _ := os.Stat(path)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("perm = %o, want 0o600", mode)
	}
}

func TestWriteCleansUpTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	// Make the destination a directory so rename fails.
	dest := filepath.Join(dir, "dest")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}

	err := Write(dest, []byte("x"), 0o600)
	if err == nil {
		t.Fatal("expected error renaming over directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".atomic-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteFailsOnMissingDir(t *testing.T) {
	if err := Write(filepath.Join(t.TempDir(), "nope", "x"), []byte("x"), 0o600); err == nil {
		t.Fatal("expected error for missing parent dir")
	}
}
