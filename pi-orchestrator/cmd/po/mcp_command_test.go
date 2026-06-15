package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPCommandIsRegisteredAndRejectsArgs(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"mcp"})
	if err != nil {
		t.Fatalf("Find(mcp) error = %v", err)
	}
	if cmd != mcpCmd {
		t.Fatalf("Find(mcp) = %v, want mcpCmd", cmd)
	}
	if err := mcpCmd.Args(mcpCmd, []string{"extra"}); err == nil || !strings.Contains(err.Error(), "po mcp accepts no positional arguments") {
		t.Fatalf("mcp Args error = %v, want no positional args error", err)
	}
}

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
