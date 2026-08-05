// Package audit records one row per request to a SQLite database and streams
// new rows to subscribers.
//
// What is deliberately absent is the point. No column holds a request body, a
// response body, or a header value, so the audit database can never become the
// leak channel the tool exists to close (AC-10). Query strings arrive already
// redacted from the proxy; this package truncates them and stores nothing else
// from the request payload.
package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Only the driver blank import. The sibling "embed" package is deprecated
	// and neither mcp-broker's audit nor its grants store imports it.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Record is one auditable request or tunnel.
//
// Every field is metadata. Adding a field that could carry request content
// would defeat AC-10, so new columns need the same scrutiny the originals got.
type Record struct {
	ID            string
	Timestamp     time.Time
	Interception  string
	Method        string
	Host          string
	Port          int
	Path          string
	Query         string
	Status        int
	DurationMS    int64
	BytesIn       int64
	BytesOut      int64
	MatchedRule   string
	Mode          string
	Injection     string
	CredentialRef string
	Outcome       string
	Error         string
}

const createSQL = `
CREATE TABLE IF NOT EXISTS audit_records (
	id             TEXT PRIMARY KEY,
	ts             INTEGER NOT NULL,
	interception   TEXT NOT NULL,
	method         TEXT,
	host           TEXT NOT NULL,
	port           INTEGER NOT NULL,
	path           TEXT,
	query          TEXT,
	status         INTEGER,
	duration_ms    INTEGER,
	bytes_in       INTEGER,
	bytes_out      INTEGER,
	matched_rule   TEXT,
	mode           TEXT,
	injection      TEXT,
	credential_ref TEXT,
	outcome        TEXT NOT NULL,
	error          TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_records(ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_host ON audit_records(host);
CREATE INDEX IF NOT EXISTS idx_audit_outcome ON audit_records(outcome);
`

// migrateSQL holds additive migrations. Each runs on every open and its error
// is discarded, which is how an "add column that may already exist" migration
// stays idempotent without a version table.
var migrateSQL = []string{}

const insertSQL = `
INSERT INTO audit_records (
	id, ts, interception, method, host, port, path, query, status,
	duration_ms, bytes_in, bytes_out, matched_rule, mode, injection,
	credential_ref, outcome, error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// maxQueryBytes bounds the stored query string.
const maxQueryBytes = 2048

// Logger writes audit records and notifies subscribers.
type Logger struct {
	db   *sql.DB
	stmt *sql.Stmt

	mu          sync.Mutex
	subscribers []*subscriberEntry
	closed      bool
}

type subscriberEntry struct {
	fn func(Record)
}

// Open creates or opens the audit database at path.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating the audit directory: %w", err)
	}
	if err := ensurePrivateFile(path); err != nil {
		return nil, fmt.Errorf("creating the audit database: %w", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("opening the audit database: %w", err)
	}

	// WAL keeps a reader (the dashboard) from blocking the writer (the request
	// path), which matters because an audit write must never slow a request.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}
	if _, err := db.Exec(createSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating the audit schema: %w", err)
	}
	for _, migration := range migrateSQL {
		_, _ = db.Exec(migration)
	}

	// The -wal and -shm sidecars are created by the statements above and carry
	// the same content, so they need the same mode as the database itself.
	if err := ensurePrivateSQLiteFiles(path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting audit database permissions: %w", err)
	}

	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("preparing the audit insert: %w", err)
	}

	return &Logger{db: db, stmt: stmt}, nil
}

// Record writes one row and notifies subscribers.
//
// A write failure is returned for the caller to log and discard. The request
// pipeline must not fail because auditing failed: losing a log line is bad,
// but breaking the agent's network because the disk filled is worse.
func (l *Logger) Record(rec Record) error {
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	// id is the primary key. A caller that forgets to set one would otherwise
	// insert a single row and have every later row silently rejected as a
	// duplicate — which reads as "nothing happened" rather than as a bug.
	if rec.ID == "" {
		rec.ID = randomID()
	}
	if len(rec.Query) > maxQueryBytes {
		rec.Query = rec.Query[:maxQueryBytes]
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.New("audit: logger is closed")
	}
	_, err := l.stmt.Exec(
		rec.ID, rec.Timestamp.UnixMilli(), rec.Interception,
		nullable(rec.Method), rec.Host, rec.Port, nullable(rec.Path), nullable(rec.Query),
		rec.Status, rec.DurationMS, rec.BytesIn, rec.BytesOut,
		nullable(rec.MatchedRule), nullable(rec.Mode), nullable(rec.Injection),
		nullable(rec.CredentialRef), rec.Outcome, nullable(rec.Error),
	)
	subs := make([]*subscriberEntry, len(l.subscribers))
	copy(subs, l.subscribers)
	l.mu.Unlock()

	if err != nil {
		return fmt.Errorf("writing an audit row: %w", err)
	}
	for _, sub := range subs {
		sub.fn(rec)
	}
	return nil
}

// nullable stores an empty string as NULL, so "the tunnel revealed no method"
// is distinguishable from "the method was empty" (AC-4).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Subscribe registers fn to receive every new record, returning an
// unsubscribe function.
func (l *Logger) Subscribe(fn func(Record)) func() {
	entry := &subscriberEntry{fn: fn}

	l.mu.Lock()
	l.subscribers = append(l.subscribers, entry)
	l.mu.Unlock()

	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, s := range l.subscribers {
			if s == entry {
				l.subscribers = append(l.subscribers[:i], l.subscribers[i+1:]...)
				return
			}
		}
	}
}

// QueryOpts filters and paginates a history query.
//
// Host is a substring match, like mcp-broker's tool filter and like what the
// dashboard's own placeholder promises. Every other filter is exact: they are
// driven by dropdowns over a fixed set of values.
type QueryOpts struct {
	Host    string
	Outcome string
	Mode    string
	Rule    string
	Limit   int
	Offset  int
}

// DefaultLimit bounds an unfiltered history query.
const DefaultLimit = 100

// MaxLimit bounds what a caller may ask for, so a dashboard request cannot
// pull the whole database into memory.
const MaxLimit = 1000

// likeEscaper quotes the characters SQLite's LIKE treats as wildcards.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLikePattern makes a user-supplied substring match literally.
//
// Without it a host filter of "%" is a wildcard that matches every row, which
// is not what someone typing into a "host contains" box asked for.
// TestQueryFilterValuesAreBound pins the "%" case.
func escapeLikePattern(s string) string { return likeEscaper.Replace(s) }

// Query returns matching records newest first, plus the total match count.
func (l *Logger) Query(ctx context.Context, opts QueryOpts) ([]Record, int, error) {
	where := "WHERE 1=1"
	var args []any

	if opts.Host != "" {
		where += ` AND host LIKE '%' || ? || '%' ESCAPE '\'`
		args = append(args, escapeLikePattern(opts.Host))
	}
	if opts.Outcome != "" {
		where += " AND outcome = ?"
		args = append(args, opts.Outcome)
	}
	if opts.Mode != "" {
		where += " AND mode = ?"
		args = append(args, opts.Mode)
	}
	if opts.Rule != "" {
		where += " AND matched_rule = ?"
		args = append(args, opts.Rule)
	}

	var total int
	if err := l.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_records "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting audit rows: %w", err)
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	offset := max(opts.Offset, 0)

	//nolint:gosec // the WHERE clause is built from fixed fragments; every
	// value is a bound parameter.
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, ts, interception, method, host, port, path, query, status,
		       duration_ms, bytes_in, bytes_out, matched_rule, mode, injection,
		       credential_ref, outcome, error
		FROM audit_records `+where+`
		ORDER BY ts DESC, id DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying audit rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading audit rows: %w", err)
	}
	return out, total, nil
}

func scanRecord(rows *sql.Rows) (Record, error) {
	var (
		rec                                                        Record
		ts                                                         int64
		method, path, query, matchedRule, mode, injection, credRef sql.NullString
		errStr                                                     sql.NullString
	)
	if err := rows.Scan(
		&rec.ID, &ts, &rec.Interception, &method, &rec.Host, &rec.Port, &path, &query,
		&rec.Status, &rec.DurationMS, &rec.BytesIn, &rec.BytesOut,
		&matchedRule, &mode, &injection, &credRef, &rec.Outcome, &errStr,
	); err != nil {
		return Record{}, fmt.Errorf("scanning an audit row: %w", err)
	}

	rec.Timestamp = time.UnixMilli(ts)
	rec.Method = method.String
	rec.Path = path.String
	rec.Query = query.String
	rec.MatchedRule = matchedRule.String
	rec.Mode = mode.String
	rec.Injection = injection.String
	rec.CredentialRef = credRef.String
	rec.Error = errStr.String
	return rec, nil
}

// Prune deletes records older than the retention window, returning how many
// were removed. A window of zero or less disables pruning.
func (l *Logger) Prune(ctx context.Context, retention time.Duration, now time.Time) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-retention).UnixMilli()

	result, err := l.db.ExecContext(ctx, "DELETE FROM audit_records WHERE ts < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning audit rows: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting pruned rows: %w", err)
	}
	return removed, nil
}

// StartPruning runs Prune on an interval until ctx is cancelled.
//
// The clock is injectable so a test can drive retention without waiting.
func (l *Logger) StartPruning(ctx context.Context, retention, interval time.Duration, now func() time.Time, onErr func(error)) {
	if retention <= 0 || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := l.Prune(ctx, retention, now()); err != nil && onErr != nil {
					onErr(err)
				}
			}
		}
	}()
}

// Close releases the database.
//
// The lock is held across the whole close, not just the flag write. Releasing
// it first would let a Record call that had already passed the closed check
// run stmt.Exec concurrently with stmt.Close.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true
	l.subscribers = nil

	if err := l.stmt.Close(); err != nil {
		_ = l.db.Close()
		return fmt.Errorf("closing the audit statement: %w", err)
	}
	if err := l.db.Close(); err != nil {
		return fmt.Errorf("closing the audit database: %w", err)
	}
	return nil
}

// randomID returns a fallback identifier for a record that arrived without
// one.
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to the clock. Uniqueness matters more than randomness
		// here: the identifier only has to keep rows distinct.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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

// ensurePrivateSQLiteFiles tightens the database and its WAL sidecars.
//
// The -wal file holds committed rows that have not been checkpointed yet, so
// leaving it world-readable would expose exactly what the database's own mode
// is protecting.
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
