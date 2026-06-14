package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/auth"
)

func TestTokenRotateReplacesPOTokenWithoutPrintingSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	path := auth.TokenPath()
	pdTokenPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "pd", "auth-token")
	if err := os.MkdirAll(filepath.Dir(pdTokenPath), 0o750); err != nil {
		t.Fatalf("mkdir pd token dir: %v", err)
	}
	if err := os.WriteFile(pdTokenPath, []byte("pd-secret"), 0o600); err != nil {
		t.Fatalf("write pd token: %v", err)
	}
	oldToken, err := auth.EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken() error = %v", err)
	}

	stdout, err := executeCommand("token", "rotate")
	if err != nil {
		t.Fatalf("token rotate error = %v", err)
	}
	newToken, err := auth.LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if oldToken == newToken {
		t.Fatal("token was not rotated")
	}
	if strings.Contains(stdout, oldToken) || strings.Contains(stdout, newToken) {
		t.Fatalf("stdout leaked token: %q", stdout)
	}
	if !strings.Contains(stdout, "New auth token written to "+path) || !strings.Contains(stdout, "Restart running po dashboard servers") {
		t.Fatalf("stdout = %q, want token path and restart note", stdout)
	}
	pdToken, err := os.ReadFile(pdTokenPath)
	if err != nil {
		t.Fatalf("read pd token: %v", err)
	}
	if string(pdToken) != "pd-secret" {
		t.Fatalf("pd token = %q, want unchanged", pdToken)
	}
}
