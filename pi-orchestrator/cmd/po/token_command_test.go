package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/auth"
)

func TestTokenRotateReplacesPOTokenWithoutPrintingSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	path := auth.TokenPath()
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
}
