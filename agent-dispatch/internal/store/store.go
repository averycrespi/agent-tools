package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

type TaskStatus string

const (
	StatusQueued    TaskStatus = "queued"
	StatusStarting  TaskStatus = "starting"
	StatusRunning   TaskStatus = "running"
	StatusSucceeded TaskStatus = "succeeded"
	StatusFailed    TaskStatus = "failed"
	StatusStopping  TaskStatus = "stopping"
	StatusStopped   TaskStatus = "stopped"
	StatusUnknown   TaskStatus = "unknown"
)

type Task struct {
	ID            string
	RepoPath      string
	RepoName      string
	Branch        string
	WorktreePath  string
	TemplateName  string
	PromptSource  string
	Prompt        string
	PromptPreview string
	Status        TaskStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Run struct {
	ID                string
	TaskID            string
	Attempt           int
	SupervisorPID     int
	PiSessionFile     string
	Status            TaskStatus
	StartedAt         time.Time
	EndedAt           sql.NullTime
	ExitCode          sql.NullInt64
	ErrorMessage      string
	ControlSocketPath string
	StdoutLogPath     string
	StderrLogPath     string
	SupervisorLogPath string
	PiEventsPath      string
}

type Event struct {
	ID          int64
	TaskID      string
	RunID       string
	Timestamp   time.Time
	Type        string
	Message     string
	PayloadJSON string
}

type Store struct {
	db *sql.DB
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS tasks (
    id             TEXT PRIMARY KEY,
    repo_path      TEXT NOT NULL,
    repo_name      TEXT NOT NULL,
    branch         TEXT NOT NULL,
    worktree_path  TEXT NOT NULL,
    template_name  TEXT NOT NULL DEFAULT '',
    prompt_source  TEXT NOT NULL,
    prompt         TEXT NOT NULL,
    prompt_preview TEXT NOT NULL,
    status         TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at);

CREATE TABLE IF NOT EXISTS runs (
    id                  TEXT PRIMARY KEY,
    task_id             TEXT NOT NULL REFERENCES tasks(id),
    attempt             INTEGER NOT NULL,
    supervisor_pid      INTEGER NOT NULL DEFAULT 0,
    pi_session_file     TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL,
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    exit_code           INTEGER,
    error_message       TEXT NOT NULL DEFAULT '',
    control_socket_path TEXT NOT NULL,
    stdout_log_path     TEXT NOT NULL,
    stderr_log_path     TEXT NOT NULL,
    supervisor_log_path TEXT NOT NULL,
    pi_events_path      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_task_id ON runs(task_id);

CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      TEXT NOT NULL REFERENCES tasks(id),
    run_id       TEXT NOT NULL REFERENCES runs(id),
    timestamp    TEXT NOT NULL,
    type         TEXT NOT NULL,
    message      TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_task_id_id ON events(task_id, id);
CREATE INDEX IF NOT EXISTS idx_events_run_id_id ON events(run_id, id);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, repo_path, repo_name, branch, worktree_path, template_name, prompt_source, prompt, prompt_preview, status, created_at, updated_at FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var tasks []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, repo_path, repo_name, branch, worktree_path, template_name, prompt_source, prompt, prompt_preview, status, created_at, updated_at FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) LatestRun(ctx context.Context, taskID string) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, task_id, attempt, supervisor_pid, pi_session_file, status, started_at, ended_at, exit_code, error_message, control_socket_path, stdout_log_path, stderr_log_path, supervisor_log_path, pi_events_path FROM runs WHERE task_id = ? ORDER BY attempt DESC LIMIT 1`, taskID)
	return scanRun(row)
}

func (s *Store) UpdateStatuses(ctx context.Context, taskID string, status TaskStatus) error {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, status, now, taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ? WHERE task_id = ?`, status, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddEvent(ctx context.Context, event Event) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (task_id, run_id, timestamp, type, message, payload_json) VALUES (?, ?, ?, ?, ?, ?)`, event.TaskID, event.RunID, formatTime(event.Timestamp), event.Type, event.Message, event.PayloadJSON)
	return err
}

func (s *Store) CreateTaskWithRun(ctx context.Context, task Task, run Run) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, repo_path, repo_name, branch, worktree_path, template_name, prompt_source, prompt, prompt_preview, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, task.RepoPath, task.RepoName, task.Branch, task.WorktreePath, task.TemplateName, task.PromptSource, task.Prompt, task.PromptPreview, task.Status, formatTime(task.CreatedAt), formatTime(task.UpdatedAt)); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, task_id, attempt, supervisor_pid, pi_session_file, status, started_at, ended_at, exit_code, error_message, control_socket_path, stdout_log_path, stderr_log_path, supervisor_log_path, pi_events_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, run.TaskID, run.Attempt, run.SupervisorPID, run.PiSessionFile, run.Status, formatTime(run.StartedAt), nullableTime(run.EndedAt), nullableInt(run.ExitCode), run.ErrorMessage, run.ControlSocketPath, run.StdoutLogPath, run.StderrLogPath, run.SupervisorLogPath, run.PiEventsPath); err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return tx.Commit()
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableTime(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return formatTime(t.Time)
}

func nullableInt(i sql.NullInt64) any {
	if !i.Valid {
		return nil
	}
	return i.Int64
}

type taskScanner interface {
	Scan(dest ...any) error
}

func scanTask(row taskScanner) (Task, error) {
	var task Task
	var created, updated string
	var status string
	if err := row.Scan(&task.ID, &task.RepoPath, &task.RepoName, &task.Branch, &task.WorktreePath, &task.TemplateName, &task.PromptSource, &task.Prompt, &task.PromptPreview, &status, &created, &updated); err != nil {
		return Task{}, err
	}
	task.Status = TaskStatus(status)
	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return task, nil
}

func scanRun(row taskScanner) (Run, error) {
	var run Run
	var started string
	var status string
	if err := row.Scan(&run.ID, &run.TaskID, &run.Attempt, &run.SupervisorPID, &run.PiSessionFile, &status, &started, &run.EndedAt, &run.ExitCode, &run.ErrorMessage, &run.ControlSocketPath, &run.StdoutLogPath, &run.StderrLogPath, &run.SupervisorLogPath, &run.PiEventsPath); err != nil {
		return Run{}, err
	}
	run.Status = TaskStatus(status)
	run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	return run, nil
}
