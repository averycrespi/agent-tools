package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateTokenWritesPOTokenUnderConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	path := TokenPath()
	oldToken, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken() error = %v", err)
	}

	newToken, err := RotateToken(path)
	if err != nil {
		t.Fatalf("RotateToken() error = %v", err)
	}
	if newToken == oldToken {
		t.Fatal("RotateToken() reused old token")
	}
	if path != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "po", "auth-token") {
		t.Fatalf("TokenPath() = %q, want po-owned path", path)
	}
	loaded, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if loaded != newToken {
		t.Fatalf("loaded token = %q, want new token", loaded)
	}
}
