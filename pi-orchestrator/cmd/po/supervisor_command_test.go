package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	posupervisor "github.com/averycrespi/agent-tools/pi-orchestrator/internal/supervisor"
)

func TestSupervisorCommandExecutesPersistedWorkflowRun(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "review", minimalCommandWorkflow("review"))
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, dir, stateDir)
	seedSupervisorCommandRun(t, stateDir)
	newStepRunner = func() posupervisor.StepRunner {
		return supervisorCommandRunner{}
	}
	t.Cleanup(func() { newStepRunner = defaultNewStepRunner })

	if _, err := executeCommand("--workflow-dir", dir, "supervisor", "--run-id", "run-1"); err != nil {
		t.Fatalf("supervisor error = %v", err)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	detail, err := db.GetWorkflowRunDetail(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if detail.Run.State != store.StateSucceeded || len(detail.Steps) != 1 || detail.Steps[0].State != store.StateSucceeded {
		t.Fatalf("detail = %+v, want succeeded run and step", detail)
	}
}

func TestSupervisorCommandMarksRunFailedWhenWorkflowDefinitionMissing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedSupervisorCommandRun(t, stateDir)

	_, err := executeCommand("supervisor", "--run-id", "run-1")
	if err == nil {
		t.Fatal("supervisor error = nil, want missing workflow error")
	}
	db, openErr := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if openErr != nil {
		t.Fatalf("open store: %v", openErr)
	}
	defer db.Close() //nolint:errcheck
	run, getErr := db.GetWorkflowRun(context.Background(), "run-1")
	if getErr != nil {
		t.Fatalf("GetWorkflowRun() error = %v", getErr)
	}
	if run.State != store.StateFailed || run.Outcome == "" {
		t.Fatalf("run = %+v, want failed state with outcome", run)
	}
}

type supervisorCommandRunner struct{}

func (supervisorCommandRunner) RunStep(context.Context, posupervisor.StepRequest) (posupervisor.StepResult, error) {
	return posupervisor.StepResult{PDTaskID: "pd-task-1", PDRunID: "pd-run-1", State: store.StateSucceeded}, nil
}

func seedSupervisorCommandRun(t *testing.T, stateDir string) {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "review", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: req.ID, Workflow: "review", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/review", WorktreePath: "/worktree", ArtifactRoot: filepath.Join(t.TempDir(), "artifacts"), State: store.StateStarting, SupervisorLogPath: filepath.Join(t.TempDir(), "supervisor.log"), CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
}
