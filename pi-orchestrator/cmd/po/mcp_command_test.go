package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPCommandDoesNotCreateWorkflowDirectoryInPreRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	workflowPath := filepath.Join(t.TempDir(), "workflows")
	t.Setenv("PO_WORKFLOW_DIR", workflowPath)

	if err := rootCmd.PersistentPreRunE(mcpCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE() error = %v", err)
	}
	if _, err := os.Stat(workflowPath); !os.IsNotExist(err) {
		t.Fatalf("workflow directory stat error = %v, want not exist", err)
	}
}
