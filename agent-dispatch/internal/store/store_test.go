package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenCreatesSchemaAndInsertTaskRun(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ad.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}

	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
}

func TestCompleteRunRecordsTerminalMetadata(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ad.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusRunning, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	require.NoError(t, st.CompleteRun(context.Background(), task.ID, StatusFailed, 7, "boom", "/tmp/session.json"))

	gotTask, err := st.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, gotTask.Status)
	gotRun, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, gotRun.Status)
	require.True(t, gotRun.EndedAt.Valid)
	require.True(t, gotRun.ExitCode.Valid)
	require.Equal(t, int64(7), gotRun.ExitCode.Int64)
	require.Equal(t, "boom", gotRun.ErrorMessage)
	require.Equal(t, "/tmp/session.json", gotRun.PiSessionFile)
}

func TestUpdateRunSupervisorPID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ad.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	require.NoError(t, st.UpdateRunSupervisorPID(context.Background(), task.ID, 4242))

	got, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 4242, got.SupervisorPID)
}
