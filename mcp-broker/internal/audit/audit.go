package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Record captures the full lifecycle of a tool call.
type Record struct {
	Timestamp time.Time      `json:"timestamp"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Verdict   string         `json:"verdict"`
	Approved  *bool          `json:"approved,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// QueryOpts controls filtering and pagination for audit queries.
type QueryOpts struct {
	Tool   string
	Limit  int
	Offset int
}

const createSQL = `
CREATE TABLE IF NOT EXISTS audit_records (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT    NOT NULL,
    tool      TEXT    NOT NULL,
    args      TEXT,
    verdict   TEXT    NOT NULL,
    approved  INTEGER,
    error     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_tool ON audit_records(tool);
`

const insertSQL = `INSERT INTO audit_records (timestamp, tool, args, verdict, approved, error)
VALUES (?, ?, ?, ?, ?, ?)`

// Logger records and queries audit entries in a SQLite database.
type Logger struct {
	mu   sync.Mutex
	db   *sql.DB
	stmt *sql.Stmt
}

// NewLogger creates a Logger that writes to the given database path.
func NewLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open audit db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}

	if _, err := db.Exec(createSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create audit table: %w", err)
	}

	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}

	return &Logger{db: db, stmt: stmt}, nil
}

// Record inserts an audit record.
func (l *Logger) Record(_ context.Context, rec Record) error {
	argsJSON, err := marshalNullable(rec.Args)
	if err != nil {
		return fmt.Errorf("marshal args: %w", err)
	}

	var approved sql.NullInt64
	if rec.Approved != nil {
		if *rec.Approved {
			approved = sql.NullInt64{Int64: 1, Valid: true}
		} else {
			approved = sql.NullInt64{Int64: 0, Valid: true}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	_, err = l.stmt.Exec(
		rec.Timestamp.Format(time.RFC3339),
		rec.Tool,
		argsJSON,
		rec.Verdict,
		approved,
		rec.Error,
	)
	if err != nil {
		return fmt.Errorf("insert audit record: %w", err)
	}
	return nil
}

// Query returns audit records matching the given filters.
func (l *Logger) Query(_ context.Context, opts QueryOpts) ([]Record, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	where := ""
	var queryArgs []any

	if opts.Tool != "" {
		where = " WHERE tool LIKE '%' || ? || '%'"
		queryArgs = append(queryArgs, opts.Tool)
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM audit_records" + where
	if err := l.db.QueryRow(countSQL, queryArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit records: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	selectSQL := "SELECT timestamp, tool, args, verdict, approved, error FROM audit_records" +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	selectArgs := make([]any, len(queryArgs), len(queryArgs)+2)
	copy(selectArgs, queryArgs)
	selectArgs = append(selectArgs, limit, opts.Offset)

	rows, err := l.db.Query(selectSQL, selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []Record
	for rows.Next() {
		var (
			ts, tool, verdict, errStr string
			argsJSON                  sql.NullString
			approved                  sql.NullInt64
		)
		if err := rows.Scan(&ts, &tool, &argsJSON, &verdict, &approved, &errStr); err != nil {
			return nil, 0, fmt.Errorf("scan audit record: %w", err)
		}

		timestamp, _ := time.Parse(time.RFC3339, ts)

		rec := Record{
			Timestamp: timestamp,
			Tool:      tool,
			Verdict:   verdict,
			Error:     errStr,
		}

		if argsJSON.Valid {
			var args map[string]any
			if err := json.Unmarshal([]byte(argsJSON.String), &args); err == nil {
				rec.Args = args
			}
		}

		if approved.Valid {
			b := approved.Int64 == 1
			rec.Approved = &b
		}

		records = append(records, rec)
	}

	if records == nil {
		records = []Record{}
	}

	return records, total, rows.Err()
}

// Close closes the prepared statement and database.
func (l *Logger) Close(_ context.Context) error {
	_ = l.stmt.Close()
	return l.db.Close()
}

func marshalNullable(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}
