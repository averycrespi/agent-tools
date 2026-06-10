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
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusQueued, StartedAt: now, AgentOptionsJSON: `{"model":"gpt-5"}`, PiArgvJSON: `["pi","--mode","rpc","--model","gpt-5"]`, EnvVarNamesJSON: `["OPENAI_API_KEY","EMPTY"]`, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}

	task.WorktreeCleanupPolicy = CleanupPolicyOnSuccess
	task.WorktreeCreatedByPD = true
	task.WorktreeCleanupStatus = CleanupStatusPending
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	gotTask, err := st.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, CleanupPolicyOnSuccess, gotTask.WorktreeCleanupPolicy)
	require.True(t, gotTask.WorktreeCreatedByPD)
	require.Equal(t, CleanupStatusPending, gotTask.WorktreeCleanupStatus)

	got, err := st.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"gpt-5"}`, got.AgentOptionsJSON)
	require.JSONEq(t, `["pi","--mode","rpc","--model","gpt-5"]`, got.PiArgvJSON)
	require.JSONEq(t, `["OPENAI_API_KEY","EMPTY"]`, got.EnvVarNamesJSON)
}

func TestOpenMigratesRunMetadataColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pd.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    repo_path TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    branch TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    prompt_source TEXT NOT NULL,
    prompt TEXT NOT NULL,
    prompt_preview TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    attempt INTEGER NOT NULL,
    supervisor_pid INTEGER NOT NULL DEFAULT 0,
    pi_session_file TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    exit_code INTEGER,
    error_message TEXT NOT NULL DEFAULT '',
    control_socket_path TEXT NOT NULL,
    stdout_log_path TEXT NOT NULL,
    stderr_log_path TEXT NOT NULL,
    pi_events_path TEXT NOT NULL
);
`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	st, err := Open(path)
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	taskColumns, err := taskTableColumns(st.db)
	require.NoError(t, err)
	require.True(t, taskColumns["worktree_cleanup_policy"])
	require.True(t, taskColumns["worktree_created_by_pd"])
	require.True(t, taskColumns["worktree_cleanup_status"])

	columns, err := runTableColumns(st.db)
	require.NoError(t, err)
	require.True(t, columns["agent_options_json"])
	require.True(t, columns["pi_argv_json"])
	require.True(t, columns["env_var_names_json"])
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

func TestRecordWorktreeCleanupKeepsTaskStatus(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	now := time.Now()
	task := Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: StatusSucceeded, WorktreeCleanupPolicy: CleanupPolicyOnSuccess, WorktreeCreatedByPD: true, WorktreeCleanupStatus: CleanupStatusPending, CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: StatusSucceeded, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, st.CreateTaskWithRun(context.Background(), task, run))

	require.NoError(t, st.RecordWorktreeCleanup(context.Background(), task.ID, CleanupStatusFailed, "dirty worktree", false))

	got, err := st.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, got.Status)
	require.Equal(t, CleanupStatusFailed, got.WorktreeCleanupStatus)
	require.Equal(t, "dirty worktree", got.WorktreeCleanupError)
	require.True(t, got.WorktreeCleanupAttemptedAt.Valid)
	require.False(t, got.WorktreeRemovedAt.Valid)
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
