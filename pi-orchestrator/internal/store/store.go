package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type State string

const (
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateStopping  State = "stopping"
	StateStopped   State = "stopped"
	StateUnknown   State = "unknown"
	StateSkipped   State = "skipped"
)

type RunRequest struct {
	ID         string
	Workflow   string
	InputsJSON string
	Source     string
	CreatedAt  time.Time
}

type WorkflowRun struct {
	ID                 string
	RequestID          string
	Workflow           string
	DefinitionHash     string
	DefinitionYAML     string
	InputsJSON         string
	Repo               string
	Branch             string
	WorktreePath       string
	ArtifactRoot       string
	State              State
	SupervisorPID      int
	SupervisorLogPath  string
	Outcome            string
	CleanupStatus      string
	CleanupError       string
	CleanupAttemptedAt sql.NullTime
	CreatedAt          time.Time
	UpdatedAt          time.Time
	EndedAt            sql.NullTime
}

type StepRun struct {
	WorkflowRunID  string
	StepID         string
	Agent          string
	ExecutionIndex int
	State          State
	PDTaskID       string
	PDRunID        string
	PDStdoutPath   string
	PDStderrPath   string
	PDEventsPath   string
	Outcome        string
	StartedAt      time.Time
	UpdatedAt      time.Time
	EndedAt        sql.NullTime
}

type Artifact struct {
	WorkflowRunID string
	StepID        string
	Name          string
	RelativePath  string
	AbsolutePath  string
	Required      bool
	Exists        bool
	UpdatedAt     time.Time
}

type WorkflowRunDetail struct {
	Run         WorkflowRun
	Steps       []StepRun
	Artifacts   []Artifact
	StepTotal   int
	StepPending int
}

type Store struct {
	db *sql.DB
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS run_requests (
    id          TEXT PRIMARY KEY,
    workflow    TEXT NOT NULL,
    inputs_json TEXT NOT NULL,
    source      TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_requests_workflow ON run_requests(workflow);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id                  TEXT PRIMARY KEY,
    request_id          TEXT NOT NULL REFERENCES run_requests(id),
    workflow            TEXT NOT NULL,
    definition_hash     TEXT NOT NULL,
    definition_yaml     TEXT NOT NULL DEFAULT '',
    inputs_json         TEXT NOT NULL,
    repo                TEXT NOT NULL,
    branch              TEXT NOT NULL,
    worktree_path       TEXT NOT NULL,
    artifact_root       TEXT NOT NULL,
    state               TEXT NOT NULL,
    supervisor_pid      INTEGER NOT NULL DEFAULT 0,
    supervisor_log_path TEXT NOT NULL,
    outcome             TEXT NOT NULL DEFAULT '',
    cleanup_status      TEXT NOT NULL DEFAULT 'not_requested',
    cleanup_error       TEXT NOT NULL DEFAULT '',
    cleanup_attempted_at TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    ended_at            TEXT
);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_state ON workflow_runs(state);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_created_at ON workflow_runs(created_at);

CREATE TABLE IF NOT EXISTS step_runs (
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id),
    step_id         TEXT NOT NULL,
    agent           TEXT NOT NULL,
    execution_index INTEGER NOT NULL,
    state           TEXT NOT NULL,
    pd_task_id      TEXT NOT NULL DEFAULT '',
    pd_run_id       TEXT NOT NULL DEFAULT '',
    pd_stdout_path  TEXT NOT NULL DEFAULT '',
    pd_stderr_path  TEXT NOT NULL DEFAULT '',
    pd_events_path  TEXT NOT NULL DEFAULT '',
    outcome         TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    ended_at        TEXT,
    PRIMARY KEY (workflow_run_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_step_runs_workflow_order ON step_runs(workflow_run_id, execution_index);

CREATE TABLE IF NOT EXISTS artifacts (
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id),
    step_id         TEXT NOT NULL,
    name            TEXT NOT NULL,
    relative_path   TEXT NOT NULL,
    absolute_path   TEXT NOT NULL,
    required        INTEGER NOT NULL,
    artifact_exists INTEGER NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (workflow_run_id, step_id, name)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_workflow_run_id ON artifacts(workflow_run_id);
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
	if err := ensureWorkflowDefinitionColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureWorkflowCleanupColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureStepRunMetadataColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func OpenReadOnly(path string) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store read-only: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open store read-only: %w", err)
	}
	return &Store{db: db}, nil
}

func ensureWorkflowDefinitionColumns(db *sql.DB) error {
	columns, err := tableColumns(db, "workflow_runs")
	if err != nil {
		return fmt.Errorf("inspect workflow_runs schema: %w", err)
	}
	if columns["definition_yaml"] {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE workflow_runs ADD COLUMN definition_yaml TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add workflow_runs definition_yaml column: %w", err)
	}
	return nil
}

func ensureWorkflowCleanupColumns(db *sql.DB) error {
	columns, err := tableColumns(db, "workflow_runs")
	if err != nil {
		return fmt.Errorf("inspect workflow_runs schema: %w", err)
	}
	addText := func(name, defaultValue string) error {
		if columns[name] {
			return nil
		}
		if _, err := db.Exec(`ALTER TABLE workflow_runs ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT '` + defaultValue + `'`); err != nil {
			return fmt.Errorf("add workflow_runs %s column: %w", name, err)
		}
		return nil
	}
	if err := addText("cleanup_status", "not_requested"); err != nil {
		return err
	}
	if err := addText("cleanup_error", ""); err != nil {
		return err
	}
	if !columns["cleanup_attempted_at"] {
		if _, err := db.Exec(`ALTER TABLE workflow_runs ADD COLUMN cleanup_attempted_at TEXT`); err != nil {
			return fmt.Errorf("add workflow_runs cleanup_attempted_at column: %w", err)
		}
	}
	return nil
}

func ensureStepRunMetadataColumns(db *sql.DB) error {
	columns, err := tableColumns(db, "step_runs")
	if err != nil {
		return fmt.Errorf("inspect step_runs schema: %w", err)
	}
	add := func(name string) error {
		if columns[name] {
			return nil
		}
		if _, err := db.Exec(`ALTER TABLE step_runs ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add step_runs %s column: %w", name, err)
		}
		return nil
	}
	for _, name := range []string{"pd_stdout_path", "pd_stderr_path", "pd_events_path"} {
		if err := add(name); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateRunRequestWithWorkflowRun(ctx context.Context, req RunRequest, run WorkflowRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create workflow run: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_requests (id, workflow, inputs_json, source, created_at) VALUES (?, ?, ?, ?, ?)`, req.ID, req.Workflow, req.InputsJSON, req.Source, formatTime(req.CreatedAt)); err != nil {
		return fmt.Errorf("insert run request: %w", err)
	}
	if run.CleanupStatus == "" {
		run.CleanupStatus = "not_requested"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id, request_id, workflow, definition_hash, definition_yaml, inputs_json, repo, branch, worktree_path, artifact_root, state, supervisor_pid, supervisor_log_path, outcome, cleanup_status, cleanup_error, cleanup_attempted_at, created_at, updated_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, run.RequestID, run.Workflow, run.DefinitionHash, run.DefinitionYAML, run.InputsJSON, run.Repo, run.Branch, run.WorktreePath, run.ArtifactRoot, string(run.State), run.SupervisorPID, run.SupervisorLogPath, run.Outcome, run.CleanupStatus, run.CleanupError, nullTimeString(run.CleanupAttemptedAt), formatTime(run.CreatedAt), formatTime(run.UpdatedAt), nullTimeString(run.EndedAt)); err != nil {
		return fmt.Errorf("insert workflow run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create workflow run: %w", err)
	}
	return nil
}

func (s *Store) GetRunRequest(ctx context.Context, id string) (RunRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, workflow, inputs_json, source, created_at FROM run_requests WHERE id = ?`, id)
	return scanRunRequest(row)
}

func (s *Store) GetWorkflowRun(ctx context.Context, id string) (WorkflowRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, request_id, workflow, definition_hash, definition_yaml, inputs_json, repo, branch, worktree_path, artifact_root, state, supervisor_pid, supervisor_log_path, outcome, cleanup_status, cleanup_error, cleanup_attempted_at, created_at, updated_at, ended_at FROM workflow_runs WHERE id = ?`, id)
	return scanWorkflowRun(row)
}

func (s *Store) ListWorkflowRuns(ctx context.Context) ([]WorkflowRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, request_id, workflow, definition_hash, definition_yaml, inputs_json, repo, branch, worktree_path, artifact_root, state, supervisor_pid, supervisor_log_path, outcome, cleanup_status, cleanup_error, cleanup_attempted_at, created_at, updated_at, ended_at FROM workflow_runs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	return scanWorkflowRuns(rows)
}

func (s *Store) listWorkflowRunsPage(ctx context.Context, limit int, offset int) ([]WorkflowRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, request_id, workflow, definition_hash, definition_yaml, inputs_json, repo, branch, worktree_path, artifact_root, state, supervisor_pid, supervisor_log_path, outcome, cleanup_status, cleanup_error, cleanup_attempted_at, created_at, updated_at, ended_at FROM workflow_runs ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	return scanWorkflowRuns(rows)
}

func (s *Store) CreateStepRun(ctx context.Context, step StepRun, artifacts []Artifact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create step run: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO step_runs (workflow_run_id, step_id, agent, execution_index, state, pd_task_id, pd_run_id, pd_stdout_path, pd_stderr_path, pd_events_path, outcome, started_at, updated_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, step.WorkflowRunID, step.StepID, step.Agent, step.ExecutionIndex, string(step.State), step.PDTaskID, step.PDRunID, step.PDStdoutPath, step.PDStderrPath, step.PDEventsPath, step.Outcome, formatTime(step.StartedAt), formatTime(step.UpdatedAt), nullTimeString(step.EndedAt)); err != nil {
		return fmt.Errorf("insert step run: %w", err)
	}
	for _, artifact := range artifacts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts (workflow_run_id, step_id, name, relative_path, absolute_path, required, artifact_exists, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, artifact.WorkflowRunID, artifact.StepID, artifact.Name, artifact.RelativePath, artifact.AbsolutePath, boolInt(artifact.Required), boolInt(artifact.Exists), formatTime(artifact.UpdatedAt)); err != nil {
			return fmt.Errorf("insert artifact: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create step run: %w", err)
	}
	return nil
}

func (s *Store) GetWorkflowRunDetail(ctx context.Context, id string) (WorkflowRunDetail, error) {
	run, err := s.GetWorkflowRun(ctx, id)
	if err != nil {
		return WorkflowRunDetail{}, err
	}
	steps, err := s.listStepRuns(ctx, id)
	if err != nil {
		return WorkflowRunDetail{}, err
	}
	artifacts, err := s.listArtifacts(ctx, id)
	if err != nil {
		return WorkflowRunDetail{}, err
	}
	stepCounts := map[State]int{}
	for _, step := range steps {
		stepCounts[step.State]++
	}
	stepTotal, stepPending := stepProgress(run.DefinitionYAML, stepCounts)
	return WorkflowRunDetail{Run: run, Steps: steps, Artifacts: artifacts, StepTotal: stepTotal, StepPending: stepPending}, nil
}

func (s *Store) UpdateStepState(ctx context.Context, workflowRunID string, stepID string, state State, outcome string, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE step_runs SET state = ?, outcome = ?, updated_at = ?, ended_at = ? WHERE workflow_run_id = ? AND step_id = ?`, string(state), outcome, formatTime(updatedAt), terminalEndedAt(state, updatedAt), workflowRunID, stepID)
	if err != nil {
		return fmt.Errorf("update step state: %w", err)
	}
	return nil
}

func (s *Store) UpdateArtifactExistence(ctx context.Context, artifacts []Artifact) error {
	for _, artifact := range artifacts {
		_, err := s.db.ExecContext(ctx, `UPDATE artifacts SET artifact_exists = ?, updated_at = ? WHERE workflow_run_id = ? AND step_id = ? AND name = ?`, boolInt(artifact.Exists), formatTime(artifact.UpdatedAt), artifact.WorkflowRunID, artifact.StepID, artifact.Name)
		if err != nil {
			return fmt.Errorf("update artifact %s: %w", artifact.Name, err)
		}
	}
	return nil
}

func (s *Store) RecordWorkflowCleanup(ctx context.Context, id string, status string, cleanupErr string, attemptedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET cleanup_status = ?, cleanup_error = ?, cleanup_attempted_at = ?, updated_at = ? WHERE id = ?`, status, cleanupErr, formatTime(attemptedAt), formatTime(attemptedAt), id)
	if err != nil {
		return fmt.Errorf("record workflow cleanup: %w", err)
	}
	return nil
}

func (s *Store) UpdateWorkflowRunSupervisorPID(ctx context.Context, id string, pid int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET supervisor_pid = ?, updated_at = ? WHERE id = ?`, pid, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update workflow supervisor pid: %w", err)
	}
	return nil
}

func (s *Store) UpdateWorkflowRunState(ctx context.Context, id string, state State, outcome string, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, outcome = ?, updated_at = ?, ended_at = ? WHERE id = ?`, string(state), outcome, formatTime(updatedAt), terminalEndedAt(state, updatedAt), id)
	if err != nil {
		return fmt.Errorf("update workflow run state: %w", err)
	}
	return nil
}

func (s *Store) RunningStepRun(ctx context.Context, workflowRunID string) (StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT workflow_run_id, step_id, agent, execution_index, state, pd_task_id, pd_run_id, pd_stdout_path, pd_stderr_path, pd_events_path, outcome, started_at, updated_at, ended_at FROM step_runs WHERE workflow_run_id = ? AND state IN ('starting', 'running', 'stopping') ORDER BY execution_index LIMIT 1`, workflowRunID)
	return scanStepRun(row)
}

func (s *Store) GetWorkflowStepRun(ctx context.Context, workflowRunID string, stepID string) (StepRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT workflow_run_id, step_id, agent, execution_index, state, pd_task_id, pd_run_id, pd_stdout_path, pd_stderr_path, pd_events_path, outcome, started_at, updated_at, ended_at FROM step_runs WHERE workflow_run_id = ? AND step_id = ?`, workflowRunID, stepID)
	return scanStepRun(row)
}

func (s *Store) DeleteWorkflowRun(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE workflow_run_id = ?`, id); err != nil {
		return fmt.Errorf("delete artifacts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM step_runs WHERE workflow_run_id = ?`, id); err != nil {
		return fmt.Errorf("delete step runs: %w", err)
	}
	var requestID string
	if err := tx.QueryRowContext(ctx, `SELECT request_id FROM workflow_runs WHERE id = ?`, id).Scan(&requestID); err != nil {
		return fmt.Errorf("select run request: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM workflow_runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workflow run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_requests WHERE id = ?`, requestID); err != nil {
		return fmt.Errorf("delete run request: %w", err)
	}
	return tx.Commit()
}

func (s *Store) listStepRuns(ctx context.Context, workflowRunID string) ([]StepRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_run_id, step_id, agent, execution_index, state, pd_task_id, pd_run_id, pd_stdout_path, pd_stderr_path, pd_events_path, outcome, started_at, updated_at, ended_at FROM step_runs WHERE workflow_run_id = ? ORDER BY execution_index`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("list step runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var steps []StepRun
	for rows.Next() {
		step, err := scanStepRun(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list step runs: %w", err)
	}
	return steps, nil
}

func (s *Store) listArtifacts(ctx context.Context, workflowRunID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_run_id, step_id, name, relative_path, absolute_path, required, artifact_exists, updated_at FROM artifacts WHERE workflow_run_id = ? ORDER BY step_id, name`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	return artifacts, nil
}

func scanWorkflowRuns(rows *sql.Rows) ([]WorkflowRun, error) {
	defer rows.Close() //nolint:errcheck
	var runs []WorkflowRun
	for rows.Next() {
		run, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	return runs, nil
}

func scanRunRequest(row interface{ Scan(...any) error }) (RunRequest, error) {
	var req RunRequest
	var createdAt string
	if err := row.Scan(&req.ID, &req.Workflow, &req.InputsJSON, &req.Source, &createdAt); err != nil {
		return RunRequest{}, fmt.Errorf("scan run request: %w", err)
	}
	req.CreatedAt = parseStoredTime(createdAt)
	return req, nil
}

func scanWorkflowRun(row interface{ Scan(...any) error }) (WorkflowRun, error) {
	var run WorkflowRun
	var state string
	var createdAt string
	var updatedAt string
	var endedAt sql.NullString
	var cleanupAttemptedAt sql.NullString
	if err := row.Scan(&run.ID, &run.RequestID, &run.Workflow, &run.DefinitionHash, &run.DefinitionYAML, &run.InputsJSON, &run.Repo, &run.Branch, &run.WorktreePath, &run.ArtifactRoot, &state, &run.SupervisorPID, &run.SupervisorLogPath, &run.Outcome, &run.CleanupStatus, &run.CleanupError, &cleanupAttemptedAt, &createdAt, &updatedAt, &endedAt); err != nil {
		return WorkflowRun{}, fmt.Errorf("scan workflow run: %w", err)
	}
	run.State = State(state)
	run.CleanupAttemptedAt = parseNullTime(cleanupAttemptedAt)
	run.CreatedAt = parseStoredTime(createdAt)
	run.UpdatedAt = parseStoredTime(updatedAt)
	run.EndedAt = parseNullTime(endedAt)
	return run, nil
}

func scanStepRun(row interface{ Scan(...any) error }) (StepRun, error) {
	var step StepRun
	var state string
	var startedAt string
	var updatedAt string
	var endedAt sql.NullString
	if err := row.Scan(&step.WorkflowRunID, &step.StepID, &step.Agent, &step.ExecutionIndex, &state, &step.PDTaskID, &step.PDRunID, &step.PDStdoutPath, &step.PDStderrPath, &step.PDEventsPath, &step.Outcome, &startedAt, &updatedAt, &endedAt); err != nil {
		return StepRun{}, fmt.Errorf("scan step run: %w", err)
	}
	step.State = State(state)
	step.StartedAt = parseStoredTime(startedAt)
	step.UpdatedAt = parseStoredTime(updatedAt)
	step.EndedAt = parseNullTime(endedAt)
	return step, nil
}

func scanArtifact(row interface{ Scan(...any) error }) (Artifact, error) {
	var artifact Artifact
	var required int
	var exists int
	var updatedAt string
	if err := row.Scan(&artifact.WorkflowRunID, &artifact.StepID, &artifact.Name, &artifact.RelativePath, &artifact.AbsolutePath, &required, &exists, &updatedAt); err != nil {
		return Artifact{}, fmt.Errorf("scan artifact: %w", err)
	}
	artifact.Required = required != 0
	artifact.Exists = exists != 0
	artifact.UpdatedAt = parseStoredTime(updatedAt)
	return artifact, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func nullTimeString(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return formatTime(t.Time)
}

func parseStoredTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func parseNullTime(value sql.NullString) sql.NullTime {
	if !value.Valid {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: parseStoredTime(value.String), Valid: true}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func terminalEndedAt(state State, at time.Time) any {
	switch state {
	case StateSucceeded, StateFailed, StateStopped, StateUnknown, StateSkipped:
		return formatTime(at)
	default:
		return nil
	}
}
