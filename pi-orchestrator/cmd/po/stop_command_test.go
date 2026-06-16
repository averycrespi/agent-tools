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
	killedSupervisorPID := 0
	killSupervisor = func(pid int) error {
		killedSupervisorPID = pid
		return nil
	}
	cleanupStopFakes(t)

	stdout, err := executeCommand("stop", "run-1")
	if err != nil {
		t.Fatalf("stop error = %v", err)
	}
	if strings.TrimSpace(stdout) != "run-1 stopped" {
		t.Fatalf("stdout = %q, want stopped line", stdout)
	}
	if len(stopped) != 1 || stopped[0].taskID != "pd-task-run-1" || stopped[0].runID != "pd-run-run-1" {
		t.Fatalf("stopped = %+v, want current backing pd run", stopped)
	}
	if killedSupervisorPID != 4321 {
		t.Fatalf("killedSupervisorPID = %d, want 4321", killedSupervisorPID)
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

func TestStopCommandAcceptsMultipleRunIDs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedStopWorkflowRunWithID(t, stateDir, "run-1", store.StateRunning, store.StateRunning)
	seedStopWorkflowRunWithID(t, stateDir, "run-2", store.StateRunning, store.StateRunning)
	stopped := []pdRunRef{}
	stopPDRun = func(_ context.Context, taskID, runID string) error {
		stopped = append(stopped, pdRunRef{taskID: taskID, runID: runID})
		return nil
	}
	killedSupervisorPIDs := []int{}
	killSupervisor = func(pid int) error {
		killedSupervisorPIDs = append(killedSupervisorPIDs, pid)
		return nil
	}
	cleanupStopFakes(t)

	stdout, err := executeCommand("stop", "run-1", "run-2")
	if err != nil {
		t.Fatalf("stop error = %v", err)
	}
	if !strings.Contains(stdout, "run-1 stopped") || !strings.Contains(stdout, "run-2 stopped") {
		t.Fatalf("stdout = %q, want both stopped lines", stdout)
	}
	if len(stopped) != 2 || stopped[0].taskID != "pd-task-run-1" || stopped[1].taskID != "pd-task-run-2" {
		t.Fatalf("stopped = %+v, want both current backing pd runs", stopped)
	}
	if len(killedSupervisorPIDs) != 2 || killedSupervisorPIDs[0] != 4321 || killedSupervisorPIDs[1] != 4322 {
		t.Fatalf("killedSupervisorPIDs = %+v, want both supervisors", killedSupervisorPIDs)
	}
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, runID := range []string{"run-1", "run-2"} {
		detail, err := db.GetWorkflowRunDetail(context.Background(), runID)
		if err != nil {
			t.Fatalf("GetWorkflowRunDetail(%s) error = %v", runID, err)
		}
		if detail.Run.State != store.StateStopped || detail.Steps[0].State != store.StateStopped {
			t.Fatalf("detail for %s = %+v, want stopped run and step", runID, detail)
		}
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

func cleanupStopFakes(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		stopPDRun = defaultStopPDRun
		killSupervisor = defaultKillSupervisor
	})
}

func seedStopWorkflowRun(t *testing.T, stateDir string, runState store.State, stepState store.State) {
	t.Helper()
	seedStopWorkflowRunWithID(t, stateDir, "run-1", runState, stepState)
}

func seedStopWorkflowRunWithID(t *testing.T, stateDir string, runID string, runState store.State, stepState store.State) {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	reqID := "req-" + runID
	supervisorPID := 4321
	if runID == "run-2" {
		supervisorPID = 4322
	}
	req := store.RunRequest{ID: reqID, Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: runID, RequestID: reqID, Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/" + runID, State: runState, SupervisorPID: supervisorPID, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: runID, StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: stepState, PDTaskID: "pd-task-" + runID, PDRunID: "pd-run-" + runID, StartedAt: now, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step); err != nil {
		t.Fatalf("create step: %v", err)
	}
}
