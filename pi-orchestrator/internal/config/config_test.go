package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesXDGPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))

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

func TestLoadAllowsConfiguredWorkflowAndArtifactDirs(t *testing.T) {
	t.Setenv("PO_WORKFLOW_DIR", "/custom/workflows")
	t.Setenv("PO_ARTIFACT_PARENT_DIR", "/custom/artifacts")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.WorkflowDir != "/custom/workflows" {
		t.Fatalf("WorkflowDir = %q", cfg.WorkflowDir)
	}
	if cfg.ArtifactParentDir != "/custom/artifacts" {
		t.Fatalf("ArtifactParentDir = %q", cfg.ArtifactParentDir)
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
