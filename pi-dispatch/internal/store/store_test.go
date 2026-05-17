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

func TestOpenDoesNotCreateEventsTable(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	var count int
	err = st.db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'`).Scan(&count)
	require.NoError(t, err)
	require.Zero(t, count)
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

func TestDeleteTaskDeletesTaskAndRuns(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusFailed, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusFailed, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	require.NoError(t, st.DeleteTask(context.Background(), task.ID))
	_, err = st.GetTask(context.Background(), task.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = st.LatestRun(context.Background(), task.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
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

func TestListTaskSummariesIncludesLatestRun(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	base := time.Now().Add(-time.Hour)
	older := Task{ID: "pd-old", RepoPath: "/repo-old", RepoName: "repo-old", Branch: "pd/old", WorktreePath: "/wt-old", PromptSource: "arg", Prompt: "old", PromptPreview: "old", Status: StatusSucceeded, CreatedAt: base, UpdatedAt: base}
	olderRun := Run{ID: "run-old", TaskID: older.ID, Attempt: 1, Status: StatusSucceeded, StartedAt: base, ControlSocketPath: "/sock-old", StdoutLogPath: "/stdout-old", StderrLogPath: "/stderr-old", PiEventsPath: "/events-old"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), older, olderRun))

	newer := Task{ID: "pd-new", RepoPath: "/repo-new", RepoName: "repo-new", Branch: "pd/new", WorktreePath: "/wt-new", PromptSource: "arg", Prompt: "new", PromptPreview: "new", Status: StatusRunning, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)}
	firstRun := Run{ID: "run-new-1", TaskID: newer.ID, Attempt: 1, Status: StatusFailed, StartedAt: base, ControlSocketPath: "/sock-1", StdoutLogPath: "/stdout-1", StderrLogPath: "/stderr-1", PiEventsPath: "/events-1"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), newer, firstRun))
	_, err = st.db.ExecContext(context.Background(), `INSERT INTO runs (id, task_id, attempt, supervisor_pid, pi_session_file, status, started_at, control_socket_path, stdout_log_path, stderr_log_path, pi_events_path) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, "run-new-2", newer.ID, 2, 1234, "/session.json", StatusRunning, formatTime(base.Add(2*time.Minute)), "/sock-2", "/stdout-2", "/stderr-2", "/events-2")
	require.NoError(t, err)

	summaries, err := st.ListTaskSummaries(context.Background())
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	require.Equal(t, "pd-new", summaries[0].Task.ID)
	require.True(t, summaries[0].LatestRun.Valid)
	require.Equal(t, "run-new-2", summaries[0].LatestRun.Run.ID)
	require.Equal(t, 2, summaries[0].LatestRun.Run.Attempt)
	require.Equal(t, "pd-old", summaries[1].Task.ID)
}
