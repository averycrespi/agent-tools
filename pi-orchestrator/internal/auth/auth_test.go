package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureTokenRejectsInsecureExistingTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write token: %v", err)
	}

	_, err := EnsureToken(path)
	if err == nil {
		t.Fatal("EnsureToken() error = nil, want insecure token mode error")
	}
}

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
