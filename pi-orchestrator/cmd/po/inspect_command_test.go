package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestPSCommandListsWorkflowRuns(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	supervisorProcessAlive = func(int) bool { return true }
	t.Cleanup(func() { supervisorProcessAlive = defaultSupervisorProcessAlive })
	seedInspectWorkflowRun(t, stateDir)

	stdout, err := executeCommand("ps")
	if err != nil {
		t.Fatalf("ps error = %v", err)
	}
	if !strings.Contains(stdout, "run-1") || !strings.Contains(stdout, "sample") || !strings.Contains(stdout, "running") {
		t.Fatalf("stdout = %q, want run id workflow and state", stdout)
	}
}

func TestStatusCommandShowsWorkflowRunDetail(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	supervisorProcessAlive = func(int) bool { return true }
	t.Cleanup(func() { supervisorProcessAlive = defaultSupervisorProcessAlive })
	seedInspectWorkflowRun(t, stateDir)

	stdout, err := executeCommand("status", "run-1")
	if err != nil {
		t.Fatalf("status error = %v", err)
	}
	for _, want := range []string{"Workflow run:", "run-1", "Workflow:", "sample", "Worktree:", "/worktree", "Artifacts:", "out", "pd-task-1", "pd-run-1", "Checks:", "target=out", "check=non_empty", "passed=true"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func seedInspectWorkflowRun(t *testing.T, stateDir string) {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/run-1", State: store.StateRunning, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: store.StateRunning, PDTaskID: "pd-task-1", PDRunID: "pd-run-1", StartedAt: now, UpdatedAt: now}
	artifacts := []store.Artifact{{WorkflowRunID: "run-1", Name: "out", RelativePath: "out.md", AbsolutePath: "/artifacts/run-1/out.md", Exists: true, UpdatedAt: now}}
	if err := db.CreateStepRun(context.Background(), step); err != nil {
		t.Fatalf("create step: %v", err)
	}
	if err := db.UpsertArtifacts(context.Background(), artifacts); err != nil {
		t.Fatalf("create artifacts: %v", err)
	}
	checks := []store.StepCheckResult{{WorkflowRunID: "run-1", StepID: "review", Kind: "artifact", Target: "out", Check: "non_empty", Passed: true, UpdatedAt: now}}
	if err := db.UpsertStepCheckResults(context.Background(), checks); err != nil {
		t.Fatalf("create check results: %v", err)
	}
}
