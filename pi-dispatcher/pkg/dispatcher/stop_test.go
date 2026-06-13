package dispatcher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	pdstore "github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
)

func TestStopTaskRunSendsStopToBackingRun(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	taskID := "pd-task-1"
	socketPath := filepath.Join(runtimeDir, "tasks", taskID+".sock")
	st := seedDispatcherStopTask(t, dbPath, taskID, socketPath, pdstore.StatusRunning)
	defer st.Close() //nolint:errcheck
	server, err := control.Listen(socketPath)
	if err != nil {
		t.Fatalf("control.Listen() error = %v", err)
	}
	defer server.Close() //nolint:errcheck
	requests := make(chan control.Request, 1)
	go server.Serve(func(req control.Request) control.Response {
		requests <- req
		return control.Response{OK: true}
	}) //nolint:errcheck

	client := NewClient(Config{DBPath: dbPath, RuntimeDir: runtimeDir})
	if err := client.StopTaskRun(context.Background(), StopTaskRunRequest{TaskID: taskID, RunID: taskID + "-run-1"}); err != nil {
		t.Fatalf("StopTaskRun() error = %v", err)
	}
	select {
	case req := <-requests:
		if req.Operation != control.OpStop {
			t.Fatalf("operation = %s, want stop", req.Operation)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stop request")
	}
}

func TestStopTaskRunRejectsTerminalTask(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	st := seedDispatcherStopTask(t, dbPath, "pd-task-1", "/tmp/missing.sock", pdstore.StatusSucceeded)
	defer st.Close() //nolint:errcheck
	client := NewClient(Config{DBPath: dbPath})
	if err := client.StopTaskRun(context.Background(), StopTaskRunRequest{TaskID: "pd-task-1", RunID: "pd-task-1-run-1"}); err == nil {
		t.Fatal("StopTaskRun() error = nil, want terminal rejection")
	}
}

func seedDispatcherStopTask(t *testing.T, dbPath string, taskID string, socketPath string, status pdstore.TaskStatus) *pdstore.Store {
	t.Helper()
	st, err := pdstore.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	task := pdstore.Task{ID: taskID, RepoPath: "/repo", RepoName: "repo", Branch: "branch", WorktreePath: "/worktree", PromptSource: "test", Prompt: "prompt", PromptPreview: "prompt", Status: status, WorktreeCleanupPolicy: pdstore.CleanupPolicyNever, WorktreeCleanupStatus: pdstore.CleanupStatusNotRequested, CreatedAt: now, UpdatedAt: now}
	run := pdstore.Run{ID: taskID + "-run-1", TaskID: taskID, Attempt: 1, Status: status, StartedAt: now, ControlSocketPath: socketPath, StdoutLogPath: "/logs/stdout", StderrLogPath: "/logs/stderr", PiEventsPath: "/logs/events"}
	if err := st.CreateTaskWithRun(context.Background(), task, run); err != nil {
		t.Fatalf("CreateTaskWithRun() error = %v", err)
	}
	return st
}
