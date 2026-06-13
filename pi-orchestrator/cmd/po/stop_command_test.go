package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestStopCommandStopsWorkflowAndCurrentPDRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedStopWorkflowRun(t, stateDir, store.StateRunning, store.StateRunning)
	stopped := []pdRunRef{}
	stopPDRun = func(_ context.Context, taskID, runID string) error {
		stopped = append(stopped, pdRunRef{taskID: taskID, runID: runID})
		return nil
	}
	t.Cleanup(func() { stopPDRun = defaultStopPDRun })

	stdout, err := executeCommand("stop", "run-1")
	if err != nil {
		t.Fatalf("stop error = %v", err)
	}
	if strings.TrimSpace(stdout) != "run-1 stopped" {
		t.Fatalf("stdout = %q, want stopped line", stdout)
	}
	if len(stopped) != 1 || stopped[0].taskID != "pd-task-1" || stopped[0].runID != "pd-run-1" {
		t.Fatalf("stopped = %+v, want current backing pd run", stopped)
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
	if detail.Run.State != store.StateStopped {
		t.Fatalf("run state = %s, want stopped", detail.Run.State)
	}
	if detail.Steps[0].State != store.StateStopped {
		t.Fatalf("step state = %s, want stopped", detail.Steps[0].State)
	}
}

func TestStopCommandRejectsTerminalWorkflowRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedStopWorkflowRun(t, stateDir, store.StateSucceeded, store.StateSucceeded)

	_, err := executeCommand("stop", "run-1")
	if err == nil || !strings.Contains(err.Error(), "workflow run run-1 is already terminal") {
		t.Fatalf("stop error = %v, want terminal error", err)
	}
}

type pdRunRef struct {
	taskID string
	runID  string
}

func seedStopWorkflowRun(t *testing.T, stateDir string, runState store.State, stepState store.State) {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/run-1", State: runState, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: stepState, PDTaskID: "pd-task-1", PDRunID: "pd-run-1", StartedAt: now, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step, nil); err != nil {
		t.Fatalf("create step: %v", err)
	}
}
