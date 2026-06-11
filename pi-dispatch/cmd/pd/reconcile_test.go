package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestReconcileStaleRunningTaskMarksUnknownWhenPIDAndSocketAreGone(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	task, run := staleRunningTask(t, filepath.Join(t.TempDir(), "missing.sock"))
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return false })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
}

func TestReconcileStaleRunningTaskMarksUnknownWhenOnlySocketFileRemains(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	socketPath := filepath.Join(t.TempDir(), "stale.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
	task, run := staleRunningTask(t, socketPath)
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return false })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
	require.NoFileExists(t, socketPath)
}

func TestReconcileKeepsRunningTaskWhenPIDExistsAndPingSucceeds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	task, run := staleRunningTask(t, filepath.Join(t.TempDir(), "task.sock"))
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		require.Equal(t, run.ControlSocketPath, path)
		require.Equal(t, control.OpPing, req.Operation)
		return control.Response{OK: true}, nil
	})

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return true })
	require.NoError(t, err)
	require.Equal(t, store.StatusRunning, got.Status)
}

func TestReconcileMarksRunningTaskUnknownWhenPingFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	task, run := staleRunningTask(t, filepath.Join(t.TempDir(), "task.sock"))
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	withControlSender(t, func(string, control.Request) (control.Response, error) {
		return control.Response{}, errors.New("ping failed")
	})

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return true })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
}

func TestReconcileStartingTaskWithinGraceSkipsPing(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	task, run := staleRunningTask(t, filepath.Join(t.TempDir(), "task.sock"))
	task.Status = store.StatusStarting
	run.Status = store.StatusStarting
	run.StartedAt = time.Now()
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	pinged := false
	withControlSender(t, func(string, control.Request) (control.Response, error) {
		pinged = true
		return control.Response{}, errors.New("unexpected ping")
	})

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return true })
	require.NoError(t, err)
	require.Equal(t, store.StatusStarting, got.Status)
	require.False(t, pinged)
}

func withControlSender(t *testing.T, fn func(string, control.Request) (control.Response, error)) {
	t.Helper()
	oldSendControlRequest := sendControlRequest
	sendControlRequest = fn
	t.Cleanup(func() { sendControlRequest = oldSendControlRequest })
}

func staleRunningTask(t *testing.T, socketPath string) (store.Task, store.Run) {
	t.Helper()
	now := time.Now()
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 4242, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: socketPath, StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	return task, run
}
