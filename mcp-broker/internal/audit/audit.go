package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Record captures the full lifecycle of a tool call.
type Record struct {
	Timestamp        time.Time      `json:"timestamp"`
	Tool             string         `json:"tool"`
	Args             map[string]any `json:"args,omitempty"`
	Verdict          string         `json:"verdict"`
	Approved         *bool          `json:"approved,omitempty"`
	DenialReason     string         `json:"denial_reason,omitempty"`
	Error            string         `json:"error,omitempty"`
	GrantID          string         `json:"grant_id,omitempty"`
	GrantName        string         `json:"grant_name,omitempty"`
	GrantFingerprint string         `json:"grant_fingerprint,omitempty"`
	GrantStatus      string         `json:"grant_status,omitempty"`
	RuleSource       string         `json:"rule_source,omitempty"`
}

// QueryOpts controls filtering and pagination for audit queries.
type QueryOpts struct {
	Tool    string
	Source  string
	Status  string
	Verdict string
	Limit   int
	Offset  int
}

const createSQL = `
CREATE TABLE IF NOT EXISTS audit_records (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    args          TEXT,
    verdict       TEXT    NOT NULL,
    approved      INTEGER,
    denial_reason TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    grant_id      TEXT    NOT NULL DEFAULT '',
    grant_name    TEXT    NOT NULL DEFAULT '',
    grant_fingerprint TEXT NOT NULL DEFAULT '',
    grant_status  TEXT    NOT NULL DEFAULT '',
    rule_source   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_tool ON audit_records(tool);
`

var migrateSQL = []string{
	`ALTER TABLE audit_records ADD COLUMN denial_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE audit_records ADD COLUMN grant_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE audit_records ADD COLUMN grant_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE audit_records ADD COLUMN grant_fingerprint TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE audit_records ADD COLUMN grant_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE audit_records ADD COLUMN rule_source TEXT NOT NULL DEFAULT ''`,
}

const insertSQL = `INSERT INTO audit_records (timestamp, tool, args, verdict, approved, denial_reason, error, grant_id, grant_name, grant_fingerprint, grant_status, rule_source)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Subscriber is called once per successful audit record insert.
// It must return quickly; hand off via channel for any real work.
type Subscriber func(rec Record)

type subscriberEntry struct {
	fn Subscriber
}

// Logger records and queries audit entries in a SQLite database.
type Logger struct {
	mu          sync.Mutex
	db          *sql.DB
	stmt        *sql.Stmt
	subscribers []*subscriberEntry
}

// NewLogger creates a Logger that writes to the given database path.
func NewLogger(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	if err := ensurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("create audit db file: %w", err)
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
	if err := ensurePrivateSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set audit db permissions: %w", err)
	}

	// Migrate: add additive columns if they do not exist yet.
	for _, migration := range migrateSQL {
		_, _ = db.Exec(migration)
	}

	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare insert: %w", err)
	}

	return &Logger{db: db, stmt: stmt}, nil
}

// Record inserts an audit record and notifies subscribers on success.
func (l *Logger) Record(ctx context.Context, rec Record) error {
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

	_, err = l.stmt.ExecContext(ctx,
		rec.Timestamp.Format(time.RFC3339),
		rec.Tool,
		argsJSON,
		rec.Verdict,
		approved,
		rec.DenialReason,
		rec.Error,
		rec.GrantID,
		rec.GrantName,
		rec.GrantFingerprint,
		rec.GrantStatus,
		rec.RuleSource,
	)
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("insert audit record: %w", err)
	}

	// Snapshot subscribers while holding the lock so that concurrent
	// unsubscribe calls cannot mutate the slice during iteration.
	snapshot := make([]*subscriberEntry, len(l.subscribers))
	copy(snapshot, l.subscribers)
	l.mu.Unlock()

	for _, entry := range snapshot {
		entry.fn(rec)
	}
	return nil
}

// Subscribe registers fn to be called after each successful Record insert.
// The returned unsubscribe function removes fn and is safe to call
// concurrently with Record and other Subscribe/unsubscribe calls.
// Calling unsubscribe more than once is a no-op.
func (l *Logger) Subscribe(fn Subscriber) (unsubscribe func()) {
	entry := &subscriberEntry{fn: fn}

	l.mu.Lock()
	l.subscribers = append(l.subscribers, entry)
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			for i, e := range l.subscribers {
				if e == entry {
					l.subscribers = append(l.subscribers[:i], l.subscribers[i+1:]...)
					return
				}
			}
		})
	}
}

func sourceCondition(source string) string {
	grantProvided := "(grant_fingerprint <> '' OR grant_status <> '' OR grant_name <> '' OR grant_id <> '')"
	grantErrored := "(grant_status <> '' AND grant_status <> 'active')"

	switch source {
	case "base":
		return "rule_source = 'base' AND NOT " + grantProvided
	case "grant":
		return "rule_source = 'grant' AND NOT " + grantErrored
	case "fall-through":
		return "NOT " + grantErrored + " AND (rule_source = 'none/default' OR (rule_source = 'base' AND " + grantProvided + "))"
	case "grant-error":
		return grantErrored
	default:
		return ""
	}
}

// likeEscaper quotes the characters SQLite's LIKE treats as wildcards.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLikePattern makes the tool filter match literally.
//
// Without it a filter of "%" is a wildcard matching every row, and "_" matches
// any single character — not what someone typing into a "Tool contains" box
// asked for. Pairs with the ESCAPE clause on the query.
func escapeLikePattern(s string) string { return likeEscaper.Replace(s) }

// Query returns audit records matching the given filters.
func (l *Logger) Query(ctx context.Context, opts QueryOpts) ([]Record, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var conditions []string
	var queryArgs []any

	if opts.Tool != "" {
		conditions = append(conditions, `tool LIKE '%' || ? || '%' ESCAPE '\'`)
		queryArgs = append(queryArgs, escapeLikePattern(opts.Tool))
	}
	if opts.Source != "" {
		if condition := sourceCondition(opts.Source); condition != "" {
			conditions = append(conditions, condition)
		}
	}
	if opts.Status != "" {
		switch opts.Status {
		case "success":
			conditions = append(conditions, "error = ''")
		case "error":
			conditions = append(conditions, "error <> ''")
		}
	}
	if opts.Verdict != "" {
		switch opts.Verdict {
		case "allow", "deny", "require-approval":
			conditions = append(conditions, "verdict = ?")
			queryArgs = append(queryArgs, opts.Verdict)
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM audit_records" + where
	if err := l.db.QueryRowContext(ctx, countSQL, queryArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit records: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	selectSQL := "SELECT timestamp, tool, args, verdict, approved, denial_reason, error, grant_id, grant_name, grant_fingerprint, grant_status, rule_source FROM audit_records" +
		where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	selectArgs := make([]any, len(queryArgs), len(queryArgs)+2)
	copy(selectArgs, queryArgs)
	selectArgs = append(selectArgs, limit, opts.Offset)

	rows, err := l.db.QueryContext(ctx, selectSQL, selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []Record
	for rows.Next() {
		var (
			ts, tool, verdict, denialReason, errStr           string
			grantID, grantName, grantFingerprint, grantStatus string
			ruleSource                                        string
			argsJSON                                          sql.NullString
			approved                                          sql.NullInt64
		)
		if err := rows.Scan(&ts, &tool, &argsJSON, &verdict, &approved, &denialReason, &errStr, &grantID, &grantName, &grantFingerprint, &grantStatus, &ruleSource); err != nil {
			return nil, 0, fmt.Errorf("scan audit record: %w", err)
		}

		timestamp, _ := time.Parse(time.RFC3339, ts)

		rec := Record{
			Timestamp:        timestamp,
			Tool:             tool,
			Verdict:          verdict,
			DenialReason:     denialReason,
			Error:            errStr,
			GrantID:          grantID,
			GrantName:        grantName,
			GrantFingerprint: grantFingerprint,
			GrantStatus:      grantStatus,
			RuleSource:       ruleSource,
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

func ensurePrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func ensurePrivateSQLiteFiles(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
