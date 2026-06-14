package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/config"
)

func TestConfigPathCommandPrintsConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	stdout, err := executeCommand("config", "path")
	if err != nil {
		t.Fatalf("config path error = %v", err)
	}
	if strings.TrimSpace(stdout) != config.ConfigFilePath() {
		t.Fatalf("stdout = %q, want config path %q", stdout, config.ConfigFilePath())
	}
	if _, err := os.Stat(config.DefaultWorkflowDir()); !os.IsNotExist(err) {
		t.Fatalf("workflow dir stat error = %v, want not exist", err)
	}
}

func TestConfigRefreshCommandWritesDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	if _, err := executeCommand("config", "refresh"); err != nil {
		t.Fatalf("config refresh error = %v", err)
	}
	data, err := os.ReadFile(config.ConfigFilePath())
	if err != nil {
		t.Fatalf("read config error = %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal config error = %v", err)
	}

	want := map[string]string{
		"database_path":       filepath.Join(os.Getenv("XDG_STATE_HOME"), "po", "po.db"),
		"workflow_dir":        filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "po", "workflows"),
		"artifact_parent_dir": filepath.Join(os.Getenv("XDG_STATE_HOME"), "po", "artifacts"),
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}
