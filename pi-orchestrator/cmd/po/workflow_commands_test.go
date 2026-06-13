package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListCommandShowsValidWorkflows(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "alpha", minimalCommandWorkflow("alpha"))
	writeWorkflow(t, dir, "beta", minimalCommandWorkflow("beta"))

	stdout, err := executeCommand("--workflow-dir", dir, "list")
	if err != nil {
		t.Fatalf("execute list: %v", err)
	}

	if !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "beta") {
		t.Fatalf("stdout = %q, want alpha and beta", stdout)
	}
}

func TestLintCommandReportsValidationError(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "broken", minimalCommandWorkflow("other"))

	_, err := executeCommand("--workflow-dir", dir, "lint", "broken")
	if err == nil {
		t.Fatal("execute lint error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "workflow name other must match filename stem broken") {
		t.Fatalf("error = %q, want filename mismatch", err.Error())
	}
}

func TestShowCommandPrintsWorkflowYAML(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "alpha", minimalCommandWorkflow("alpha"))

	stdout, err := executeCommand("--workflow-dir", dir, "show", "alpha")
	if err != nil {
		t.Fatalf("execute show: %v", err)
	}

	if !strings.Contains(stdout, "name: alpha") || !strings.Contains(stdout, "steps:") {
		t.Fatalf("stdout = %q, want workflow YAML", stdout)
	}
}

func TestShowCommandRejectsWorkflowPathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := executeCommand("--workflow-dir", dir, "show", "../outside")
	if err == nil || !strings.Contains(err.Error(), "workflow name must not contain path separators") {
		t.Fatalf("show error = %v, want path separator rejection", err)
	}
}

func executeCommand(args ...string) (string, error) {
	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(args)
	defer func() {
		rootCmd.SetOut(os.Stdout)
		rootCmd.SetErr(os.Stderr)
		rootCmd.SetArgs(nil)
		workflowDir = ""
		runInputs = nil
		cleanupDryRun = false
		supervisorRunID = ""
		newStepRunner = defaultNewStepRunner
	}()
	err := Execute()
	return stdout.String(), err
}

func writeWorkflow(t *testing.T, dir string, name string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

func minimalCommandWorkflow(name string) string {
	return `name: ` + name + `
repo: repo
agents:
  runner:
    model: gpt-5.1-codex
steps:
  - id: run
    agent: runner
    prompt: run
`
}
