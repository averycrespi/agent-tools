package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/auth"
)

func TestDashboardURLUsesPOToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	token, err := auth.EnsureToken(auth.TokenPath())
	if err != nil {
		t.Fatalf("EnsureToken() error = %v", err)
	}

	url := dashboardURL("127.0.0.1", 8400, token)
	if !strings.HasPrefix(url, "http://localhost:8400/dashboard/?token=") || !strings.Contains(url, token) {
		t.Fatalf("url = %q, want localhost dashboard token URL", url)
	}
}

func TestValidateDashboardHostRejectsNonLoopback(t *testing.T) {
	if err := validateDashboardHost("example.com"); err == nil {
		t.Fatal("validateDashboardHost(example.com) error = nil, want non-loopback error")
	}
}

func TestDashboardCommandDefinesLoopbackFlags(t *testing.T) {
	if dashboardCmd.Flags().Lookup("host") == nil || dashboardCmd.Flags().Lookup("port") == nil || dashboardCmd.Flags().Lookup("no-open") == nil {
		t.Fatalf("dashboard flags missing")
	}
}
