package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenCreatesSchemaAndInsertTaskRun(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}

	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
}

func TestCompleteRunRecordsTerminalMetadata(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusRunning, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
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

func TestDeleteTaskDeletesTaskRunsAndEvents(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusFailed, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusFailed, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, st.AddEvent(context.Background(), Event{TaskID: task.ID, RunID: run.ID, Timestamp: now, Type: "test", Message: "event"}))

	require.NoError(t, st.DeleteTask(context.Background(), task.ID))
	_, err = st.GetTask(context.Background(), task.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = st.LatestRun(context.Background(), task.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	events, err := st.ListEvents(context.Background(), task.ID)
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestUpdateRunSupervisorPID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	require.NoError(t, st.UpdateRunSupervisorPID(context.Background(), task.ID, 4242))

	got, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 4242, got.SupervisorPID)
}
