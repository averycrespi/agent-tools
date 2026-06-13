package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestWaitCommandReturnsSuccessForSucceededWorkflowRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedWaitLogsWorkflowRun(t, stateDir, store.StateSucceeded)
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = time.Second })

	stdout, err := executeCommand("wait", "run-1", "--timeout", "50ms")
	if err != nil {
		t.Fatalf("wait error = %v", err)
	}
	if strings.TrimSpace(stdout) != "run-1 succeeded" {
		t.Fatalf("stdout = %q, want succeeded line", stdout)
	}
}

func TestWaitCommandReturnsErrorForFailedWorkflowRun(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedWaitLogsWorkflowRun(t, stateDir, store.StateFailed)
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = time.Second })

	stdout, err := executeCommand("wait", "run-1", "--timeout", "50ms")
	if err == nil {
		t.Fatal("wait error = nil, want failed workflow error")
	}
	if strings.TrimSpace(stdout) != "run-1 failed" {
		t.Fatalf("stdout = %q, want failed line", stdout)
	}
	if !strings.Contains(err.Error(), "workflow run run-1 failed") {
		t.Fatalf("error = %q, want failed workflow", err.Error())
	}
}

func TestWaitCommandMarksMissingSupervisorUnknown(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	seedWaitLogsWorkflowRun(t, stateDir, store.StateRunning)
	waitPollInterval = time.Millisecond
	supervisorProcessAlive = func(int) bool { return false }
	t.Cleanup(func() {
		waitPollInterval = time.Second
		supervisorProcessAlive = defaultSupervisorProcessAlive
	})

	stdout, err := executeCommand("wait", "run-1", "--timeout", "50ms")
	if err == nil {
		t.Fatal("wait error = nil, want unknown workflow error")
	}
	if strings.TrimSpace(stdout) != "run-1 unknown" {
		t.Fatalf("stdout = %q, want unknown line", stdout)
	}
	if !strings.Contains(err.Error(), "workflow run run-1 unknown") {
		t.Fatalf("error = %q, want unknown workflow", err.Error())
	}
}

func TestLogsCommandShowsSupervisorLogAndPDLogPointers(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	cfg = testConfig(t, t.TempDir(), stateDir)
	logPath := seedWaitLogsWorkflowRun(t, stateDir, store.StateSucceeded)
	if err := os.WriteFile(logPath, []byte("supervisor started\nstep complete\n"), 0o600); err != nil {
		t.Fatalf("write supervisor log: %v", err)
	}

	stdout, err := executeCommand("logs", "run-1")
	if err != nil {
		t.Fatalf("logs error = %v", err)
	}
	for _, want := range []string{"supervisor started", "step complete", "Backing pd logs:", "review", "pd-task-1", "pd-run-1", "pd logs pd-task-1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func seedWaitLogsWorkflowRun(t *testing.T, stateDir string, state store.State) string {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, "po", "po.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	logDir := filepath.Join(stateDir, "po", "runs", "run-1")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	logPath := filepath.Join(logDir, "supervisor.log")
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/run-1", State: state, SupervisorLogPath: logPath, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: state, PDTaskID: "pd-task-1", PDRunID: "pd-run-1", StartedAt: now, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step, nil); err != nil {
		t.Fatalf("create step: %v", err)
	}
	return logPath
}
