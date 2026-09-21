package invocation

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"
)

// VerifyLegacyEvidence validates every retained legacy row without migration,
// creating sidecars, or treating missing post-invocation storage as empty.
func VerifyLegacyEvidence(ctx context.Context, path string, schema int) (result error) {
	if schema < 9 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uri := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, db.Close()) }()
	var high int64
	if err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='invocations'),0)`).Scan(&high); err != nil {
		return err
	}
	query := invocationSelect
	if schema < 17 {
		query = strings.Replace(query, "failure_diagnostics", "NULL AS failure_diagnostics", 1)
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY insertion_sequence LIMIT ?`, invocationLimit()+1)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, rows.Close()) }()
	var count, previous int64
	for rows.Next() {
		record, err := scanInvocation(rows)
		if err != nil {
			return err
		}
		count++
		if count > invocationLimit() || record.Sequence <= previous || record.Sequence > high || !validStoredInvocation(record) {
			return ErrInvalidState
		}
		previous = record.Sequence
	}
	if high < count {
		return ErrInvalidState
	}
	return rows.Err()
}

// VerifyTrafficFile verifies a digest-bound closed generation without sidecars,
// new storage, write authority, receipts or replay state.
func VerifyTrafficFile(ctx context.Context, path, installation, generation string, config TrafficConfig) (result error) {
	if !config.valid() || !validOpaqueInvocationID(installation) || !validOpaqueInvocationID(generation) {
		return ErrInvalidInput
	}
	if err := trafficFiles(path, config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uri := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, db.Close()) }()
	var app, version int
	var integrity string
	if err = db.QueryRowContext(ctx, `SELECT (SELECT application_id FROM pragma_application_id),(SELECT user_version FROM pragma_user_version)`).Scan(&app, &version); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if app != trafficApplicationID || version != 1 || integrity != "ok" {
		return ErrInvalidState
	}
	return (&TrafficStore{db: db, path: path, config: config}).validateTrafficEvidence(ctx, installation, generation)
}
