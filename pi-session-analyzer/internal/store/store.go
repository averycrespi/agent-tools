// Package store persists scrubbed normalized session data in private SQLite storage.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/scrub"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const schema = `
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY, source_path TEXT NOT NULL UNIQUE, source_size INTEGER NOT NULL,
 source_mtime_ns INTEGER NOT NULL, schema_version INTEGER NOT NULL, schema_drift INTEGER NOT NULL,
 timestamp TEXT NOT NULL, started_at_unix INTEGER, cwd TEXT NOT NULL, total_records INTEGER NOT NULL,
 malformed_records INTEGER NOT NULL, unknown_records INTEGER NOT NULL, ingested_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS messages (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 parent_id TEXT NOT NULL, source_line INTEGER NOT NULL, timestamp TEXT NOT NULL, role TEXT NOT NULL,
 model TEXT NOT NULL, stop_reason TEXT NOT NULL, text TEXT NOT NULL,
 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL,
 cache_read_tokens INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL, cost REAL NOT NULL,
 PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS tool_calls (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 message_id TEXT NOT NULL, source_line INTEGER NOT NULL, name TEXT NOT NULL, arguments TEXT NOT NULL,
 normalized_target TEXT NOT NULL DEFAULT '', PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS tool_results (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 message_id TEXT NOT NULL, call_id TEXT NOT NULL, source_line INTEGER NOT NULL, name TEXT NOT NULL,
 content TEXT NOT NULL, content_hash TEXT NOT NULL, is_error INTEGER,
 PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS events (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 source_line INTEGER NOT NULL, type TEXT NOT NULL, value TEXT NOT NULL, details TEXT NOT NULL,
 tokens_before INTEGER NOT NULL, PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS custom_state (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 source_line INTEGER NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS custom_messages (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, id TEXT NOT NULL,
 source_line INTEGER NOT NULL, type TEXT NOT NULL, kind TEXT NOT NULL, content TEXT NOT NULL,
 details TEXT NOT NULL, PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS findings (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 detector TEXT NOT NULL, classification TEXT NOT NULL CHECK(classification IN ('structural','heuristic')),
 severity TEXT NOT NULL CHECK(severity IN ('error','warn','info')), summary TEXT NOT NULL,
 first_evidence_id TEXT NOT NULL, source_line INTEGER NOT NULL, details TEXT NOT NULL,
 generation INTEGER NOT NULL, stale INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_finding_identity ON findings(session_id,detector,first_evidence_id);
CREATE TABLE IF NOT EXISTS detector_runs (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, detector TEXT NOT NULL,
 generation INTEGER NOT NULL, status TEXT NOT NULL CHECK(status IN ('success','failed')),
 error_summary TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT NOT NULL,
 PRIMARY KEY(session_id,detector)
);
CREATE INDEX IF NOT EXISTS idx_messages_session_line ON messages(session_id,source_line);
CREATE INDEX IF NOT EXISTS idx_findings_order ON findings(detector,session_id,first_evidence_id);
`

// SourceMeta identifies whether a source file changed.
type SourceMeta struct {
	Path      string
	Size      int64
	ModTimeNS int64
}

// SessionRow is a compact session listing.
type SessionRow struct {
	ID, Timestamp, CWD, SourcePath string
	SchemaDrift, TotalRecords      int
}

// Store owns analyzer SQLite state.
type Store struct {
	*Reader
	db   *sql.DB
	path string
}

// Open opens the database, creates only its analyzer-owned leaf, and repairs private modes.
func Open(path string) (*Store, error) {
	leaf := filepath.Dir(path)
	info, statErr := os.Stat(leaf)
	switch {
	case os.IsNotExist(statErr):
		if err := os.MkdirAll(filepath.Dir(leaf), 0o750); err != nil {
			return nil, fmt.Errorf("create data directory ancestors: %w", err)
		}
		if err := os.Mkdir(leaf, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
		if err := os.Chmod(leaf, 0o700); err != nil { //nolint:gosec // A newly created analyzer leaf must be private.
			return nil, fmt.Errorf("set data directory permissions: %w", err)
		}
	case statErr != nil:
		return nil, fmt.Errorf("inspect data directory: %w", statErr)
	case !info.IsDir():
		return nil, fmt.Errorf("database parent is not a directory: %s", leaf)
	case filepath.Base(leaf) == "pi-session-analyzer":
		if err := os.Chmod(leaf, 0o700); err != nil { //nolint:gosec // Only the named analyzer-owned leaf is repaired.
			return nil, fmt.Errorf("set data directory permissions: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // The caller-selected local database path is the intended file.
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("set database permissions: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;" + schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	if err = migrateCanonicalSessionStarts(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	s := &Store{Reader: newSQLReader(db), db: db, path: path}
	if err := s.repairPermissions(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) repairPermissions() error {
	for _, path := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("set SQLite permissions: %w", err)
		}
	}
	return nil
}

func (s *Store) SourceUnchanged(ctx context.Context, meta SourceMeta) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE source_path=? AND source_size=? AND source_mtime_ns=?`, scrub.Scrub(meta.Path), meta.Size, meta.ModTimeNS).Scan(&count)
	return count == 1, err
}

// ReplaceSession transactionally replaces all normalized rows for a source session.
func (s *Store) ReplaceSession(ctx context.Context, in ingest.Session, meta SourceMeta) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sessionID := scrub.Scrub(in.ID)
	sourcePath := scrub.Scrub(meta.Path)
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE source_path=? AND id<>?`, sourcePath, sessionID); err != nil {
		return fmt.Errorf("delete replaced source session: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,source_path,source_size,source_mtime_ns,schema_version,schema_drift,timestamp,started_at_unix,cwd,total_records,malformed_records,unknown_records) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_path=excluded.source_path,source_size=excluded.source_size,source_mtime_ns=excluded.source_mtime_ns,schema_version=excluded.schema_version,schema_drift=excluded.schema_drift,timestamp=excluded.timestamp,started_at_unix=excluded.started_at_unix,cwd=excluded.cwd,total_records=excluded.total_records,malformed_records=excluded.malformed_records,unknown_records=excluded.unknown_records,ingested_at=CURRENT_TIMESTAMP`, sessionID, sourcePath, meta.Size, meta.ModTimeNS, in.Version, in.Stats.SchemaDrift, scrub.Scrub(in.Timestamp), in.StartedAtUnix, scrub.Scrub(in.CWD), in.Stats.Total, in.Stats.Malformed, in.Stats.Unknown); err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	for _, table := range []string{"messages", "tool_calls", "tool_results", "events", "custom_state", "custom_messages"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE session_id=?`, sessionID); err != nil { //nolint:gosec // table names are a closed internal list.
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	for _, m := range in.Messages {
		_, err = tx.ExecContext(ctx, `INSERT INTO messages VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sessionID, scrub.Scrub(m.ID), scrub.Scrub(m.ParentID), m.SourceLine, scrub.Scrub(m.Timestamp), scrub.Scrub(m.Role), scrub.Scrub(m.Model), scrub.Scrub(m.StopReason), scrub.Scrub(m.Text), m.InputTokens, m.OutputTokens, m.ReasoningTokens, m.CacheReadTokens, m.CacheWriteTokens, m.Cost)
		if err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
	}
	for _, c := range in.ToolCalls {
		_, err = tx.ExecContext(ctx, `INSERT INTO tool_calls(session_id,id,message_id,source_line,name,arguments) VALUES(?,?,?,?,?,?)`, sessionID, scrub.Scrub(c.ID), scrub.Scrub(c.MessageID), c.SourceLine, scrub.Scrub(c.Name), scrub.JSON(c.Arguments))
		if err != nil {
			return fmt.Errorf("insert tool call: %w", err)
		}
	}
	for _, r := range in.ToolResults {
		content := scrub.JSON(r.Content)
		var isError any
		if r.IsError != nil {
			if *r.IsError {
				isError = 1
			} else {
				isError = 0
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tool_results(session_id,id,message_id,call_id,source_line,name,content,content_hash,is_error) VALUES(?,?,?,?,?,?,?,?,?)`, sessionID, scrub.Scrub(r.ID), scrub.Scrub(r.MessageID), scrub.Scrub(r.CallID), r.SourceLine, scrub.Scrub(r.Name), content, contentHash(content), isError)
		if err != nil {
			return fmt.Errorf("insert tool result: %w", err)
		}
	}
	for _, e := range in.Events {
		_, err = tx.ExecContext(ctx, `INSERT INTO events VALUES(?,?,?,?,?,?,?)`, sessionID, scrub.Scrub(e.ID), e.SourceLine, scrub.Scrub(e.Type), scrub.Scrub(e.Value), scrub.JSON(e.Details), e.TokensBefore)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
	}
	for _, c := range in.CustomStates {
		_, err = tx.ExecContext(ctx, `INSERT INTO custom_state VALUES(?,?,?,?,?,?)`, sessionID, scrub.Scrub(c.ID), c.SourceLine, scrub.Scrub(c.Type), scrub.Scrub(c.Status), scrub.JSON(c.Data))
		if err != nil {
			return fmt.Errorf("insert custom state: %w", err)
		}
	}
	for _, c := range in.CustomMessages {
		_, err = tx.ExecContext(ctx, `INSERT INTO custom_messages VALUES(?,?,?,?,?,?,?)`, sessionID, scrub.Scrub(c.ID), c.SourceLine, scrub.Scrub(c.Type), scrub.Scrub(c.Kind), scrub.Scrub(c.Content), scrub.JSON(c.Details))
		if err != nil {
			return fmt.Errorf("insert custom message: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit session replacement: %w", err)
	}
	return s.repairPermissions()
}

func (s *Reader) ListSessions(ctx context.Context, limit int, cwdFilter string) ([]SessionRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.query.QueryContext(ctx, `SELECT id,timestamp,cwd,source_path,schema_drift,total_records FROM sessions WHERE cwd LIKE '%' || ? || '%' ORDER BY timestamp DESC,id LIMIT ?`, cwdFilter, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []SessionRow{}
	for rows.Next() {
		var row SessionRow
		if err := rows.Scan(&row.ID, &row.Timestamp, &row.CWD, &row.SourcePath, &row.SchemaDrift, &row.TotalRecords); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ResolveSession resolves an exact ID or unique prefix.
func (s *Reader) ResolveSession(ctx context.Context, prefix string) (string, error) {
	rows, err := s.query.QueryContext(ctx, `SELECT id FROM sessions WHERE id=? OR id LIKE ? ORDER BY id LIMIT 2`, prefix, prefix+"%")
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("session %q not found", prefix)
	}
	for _, id := range ids {
		if id == prefix {
			return id, nil
		}
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("session prefix %q is ambiguous", prefix)
	}
	return ids[0], nil
}

func migrateCanonicalSessionStarts(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000; BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()

	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return err
	}
	hasColumn := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "started_at_unix" {
			hasColumn = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !hasColumn {
		if _, err = conn.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN started_at_unix INTEGER`); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_sessions_started_at_unix ON sessions(started_at_unix)`); err != nil {
		return err
	}
	rows, err = conn.QueryContext(ctx, `SELECT id,timestamp FROM sessions WHERE started_at_unix IS NULL AND timestamp<>''`)
	if err != nil {
		return err
	}
	type backfill struct {
		id   string
		unix int64
	}
	var updates []backfill
	for rows.Next() {
		var id, timestamp string
		if err = rows.Scan(&id, &timestamp); err != nil {
			_ = rows.Close()
			return err
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp); parseErr == nil {
			updates = append(updates, backfill{id: id, unix: parsed.Unix()})
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err = conn.ExecContext(ctx, `UPDATE sessions SET started_at_unix=? WHERE id=?`, update.unix, update.id); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func contentHash(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
