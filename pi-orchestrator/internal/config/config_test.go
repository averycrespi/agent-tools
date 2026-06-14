package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesXDGPaths(t *testing.T) {
	setXDG(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.WorkflowDir != filepath.Join(mustEnv(t, "XDG_CONFIG_HOME"), "po", "workflows") {
		t.Fatalf("WorkflowDir = %q", cfg.WorkflowDir)
	}
	if cfg.DBPath() != filepath.Join(mustEnv(t, "XDG_STATE_HOME"), "po", "po.db") {
		t.Fatalf("DBPath() = %q", cfg.DBPath())
	}
	if cfg.ArtifactParentDir != filepath.Join(mustEnv(t, "XDG_STATE_HOME"), "po", "artifacts") {
		t.Fatalf("ArtifactParentDir = %q", cfg.ArtifactParentDir)
	}
	if cfg.RunLogDir("run-1") != filepath.Join(mustEnv(t, "XDG_STATE_HOME"), "po", "runs", "run-1") {
		t.Fatalf("RunLogDir() = %q", cfg.RunLogDir("run-1"))
	}
	if cfg.RuntimeDir != filepath.Join(mustEnv(t, "XDG_RUNTIME_DIR"), "po") {
		t.Fatalf("RuntimeDir = %q", cfg.RuntimeDir)
	}
}

func TestLoadUsesConfigFile(t *testing.T) {
	setXDG(t)
	writeConfig(t, `{"database_path":"/custom/po.db","workflow_dir":"/custom/workflows","artifact_parent_dir":"/custom/artifacts"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DBPath() != "/custom/po.db" {
		t.Fatalf("DBPath() = %q", cfg.DBPath())
	}
	if cfg.WorkflowDir != "/custom/workflows" {
		t.Fatalf("WorkflowDir = %q", cfg.WorkflowDir)
	}
	if cfg.ArtifactParentDir != "/custom/artifacts" {
		t.Fatalf("ArtifactParentDir = %q", cfg.ArtifactParentDir)
	}
}

func TestLoadExpandsTildeInConfigFile(t *testing.T) {
	setXDG(t)
	writeConfig(t, `{"database_path":"~/state/po.db","workflow_dir":"~/workflows","artifact_parent_dir":"~/artifacts"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	if cfg.DBPath() != filepath.Join(home, "state", "po.db") {
		t.Fatalf("DBPath() = %q", cfg.DBPath())
	}
	if cfg.WorkflowDir != filepath.Join(home, "workflows") {
		t.Fatalf("WorkflowDir = %q", cfg.WorkflowDir)
	}
	if cfg.ArtifactParentDir != filepath.Join(home, "artifacts") {
		t.Fatalf("ArtifactParentDir = %q", cfg.ArtifactParentDir)
	}
}

func TestLoadEnvironmentOverridesConfigFile(t *testing.T) {
	setXDG(t)
	writeConfig(t, `{"workflow_dir":"/config/workflows","artifact_parent_dir":"/config/artifacts"}`)
	t.Setenv("PO_WORKFLOW_DIR", "/env/workflows")
	t.Setenv("PO_ARTIFACT_PARENT_DIR", "/env/artifacts")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.WorkflowDir != "/env/workflows" {
		t.Fatalf("WorkflowDir = %q", cfg.WorkflowDir)
	}
	if cfg.ArtifactParentDir != "/env/artifacts" {
		t.Fatalf("ArtifactParentDir = %q", cfg.ArtifactParentDir)
	}
}

func TestRefreshWritesDefaultConfig(t *testing.T) {
	setXDG(t)

	if err := Refresh(); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	got := readConfigMap(t)

	want := map[string]string{
		"database_path":       filepath.Join(mustEnv(t, "XDG_STATE_HOME"), "po", "po.db"),
		"workflow_dir":        filepath.Join(mustEnv(t, "XDG_CONFIG_HOME"), "po", "workflows"),
		"artifact_parent_dir": filepath.Join(mustEnv(t, "XDG_STATE_HOME"), "po", "artifacts"),
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestRefreshDoesNotWriteEnvironmentOverrides(t *testing.T) {
	setXDG(t)
	t.Setenv("PO_WORKFLOW_DIR", "/env/workflows")
	t.Setenv("PO_ARTIFACT_PARENT_DIR", "/env/artifacts")

	if err := Refresh(); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	got := readConfigMap(t)

	if got["workflow_dir"] == "/env/workflows" {
		t.Fatalf("workflow_dir wrote environment override")
	}
	if got["artifact_parent_dir"] == "/env/artifacts" {
		t.Fatalf("artifact_parent_dir wrote environment override")
	}
}

func readConfigMap(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(ConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return got
}

func setXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
}

func writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(ConfigDir(), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(ConfigFilePath(), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is empty", key)
	}
	return value
}
