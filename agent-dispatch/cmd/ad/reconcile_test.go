package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	"github.com/stretchr/testify/require"
)

func TestReconcileStaleRunningTaskMarksUnknownWhenPIDAndSocketAreGone(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "ad.db"))
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck

	now := time.Now()
	task := store.Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 4242, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: filepath.Join(t.TempDir(), "missing.sock"), StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))

	got, err := reconcileTask(context.Background(), db, task, run, func(int) bool { return false })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
}
