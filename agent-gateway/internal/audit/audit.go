// Package audit implements the per-request audit log for agent-gateway.
// It writes to the requests table (migration 4) and exposes a Logger interface
// for recording, querying, and pruning audit entries.
package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvariantViolation is returned by Record when the entry violates the
// invariant: credential_ref IS NOT NULL ⟺ injection = 'applied'.
var ErrInvariantViolation = errors.New("audit: credential_ref IS NOT NULL ⟺ injection = 'applied'")

// Entry is a single row in the requests table — the "5-column story" plus
// all metadata columns. Pointer fields are nullable.
type Entry struct {
	// ID is the ULID assigned at request decode time.
	ID string
	// TS is the timestamp of the request (unix ms precision).
	TS time.Time
	// Agent is the authenticated agent name (NULL if unauthenticated).
	Agent *string
	// Interception is "tunnel" or "mitm".
	Interception string
	// Method is the HTTP method (NULL for tunnel rows).
	Method *string
	// Host is the CONNECT target hostname.
	Host string
	// Path is the request path (NULL for tunnel rows).
	Path *string
	// Query is the raw query string (NULL if absent).
	Query *string
	// Status is the upstream HTTP status code (NULL on transport error or blocked).
	Status *int
	// DurationMS is the request duration in milliseconds.
	DurationMS int64
	// BytesIn is the number of bytes received from upstream.
	BytesIn int64
	// BytesOut is the number of bytes sent to upstream.
	BytesOut int64
	// MatchedRule is the name of the matched rule (NULL if no match).
	MatchedRule *string
	// RuleVerdict is "allow", "deny", or "require-approval" (NULL if no match).
	RuleVerdict *string
	// Approval is "approved", "denied", or "timed-out" (NULL if n/a).
	Approval *string
	// Injection is "applied" or "failed" (NULL if n/a).
	Injection *string
	// Outcome is "forwarded" or "blocked".
	Outcome string
	// CredentialRef is the secret name used for injection (e.g. "gh_bot").
	// NULL iff injection != "applied".
	CredentialRef *string
	// CredentialScope is the scope of the resolved secret (NULL iff CredentialRef is NULL).
	CredentialScope *string
	// Error is a structured error tag (e.g. "secret_unresolved").
	Error *string
}

// Filter narrows the results of a Query call.
// Nil pointer fields are ignored (not filtered on).
type Filter struct {
	// After filters to rows with ts > After.
	After *time.Time
	// Before filters to rows with ts < Before.
	Before *time.Time
	// Agent filters to rows for this agent name.
	Agent *string
	// Host filters to rows for this exact host.
	Host *string
	// Rule filters to rows that matched this rule name.
	Rule *string
	// Limit is the maximum number of rows to return (default: no limit).
	Limit *int
	// Offset is the number of rows to skip (for pagination, default: 0).
	Offset *int
}

// Logger is the interface for recording, querying, and pruning audit entries.
// All methods are safe for concurrent use.
//
// Record never returns an error to the caller on the hot path; callers should
// log and discard any error. The only exception is ErrInvariantViolation,
// which indicates a programming error and should always surface.
//
// Query returns entries in descending timestamp order.
//
// Prune removes all entries with ts strictly less than before (not ≤).
// It returns the number of rows deleted.
type Logger interface {
	Record(ctx context.Context, entry Entry) error
	Query(ctx context.Context, filter Filter) ([]Entry, error)
	Prune(ctx context.Context, before time.Time) (int, error)
}

const insertSQL = `
INSERT INTO requests (
  id, ts, agent, interception, method, host, path, query,
  status, duration_ms, bytes_in, bytes_out,
  matched_rule, rule_verdict, approval, injection, outcome,
  credential_ref, credential_scope, error
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?, ?, ?, ?,
  ?, ?, ?
)`

// sqlLogger is the production Logger backed by a *sql.DB.
type sqlLogger struct {
	db   *sql.DB
	stmt *sql.Stmt
}

// NewLogger constructs a Logger backed by db. The INSERT statement is prepared
// eagerly so that per-request Record calls never need to reparse SQL.
//
// NewLogger returns the Logger interface so callers are insulated from the
// concrete type.
func NewLogger(db *sql.DB) Logger {
	stmt, err := db.Prepare(insertSQL)
	if err != nil {
		// If Prepare fails here the migration hasn't run yet — panic with a
		// clear message so the programmer knows rather than silently dropping rows.
		panic(fmt.Sprintf("audit.NewLogger: prepare INSERT: %v", err))
	}
	return &sqlLogger{db: db, stmt: stmt}
}

// Record writes e to the requests table. It enforces the invariant
//
//	credential_ref IS NOT NULL ⟺ injection = 'applied'
//
// before attempting the INSERT, returning ErrInvariantViolation on breach.
// Any other database error is returned wrapped; callers on the hot path should
// log and discard it.
func (l *sqlLogger) Record(ctx context.Context, e Entry) error {
	// Enforce invariant in Go before touching the DB.
	if err := checkInvariant(e); err != nil {
		return err
	}

	tsUnix := e.TS.UnixMilli()

	_, err := l.stmt.ExecContext(ctx,
		e.ID,
		tsUnix,
		e.Agent,
		e.Interception,
		e.Method,
		e.Host,
		e.Path,
		e.Query,
		e.Status,
		e.DurationMS,
		e.BytesIn,
		e.BytesOut,
		e.MatchedRule,
		e.RuleVerdict,
		e.Approval,
		e.Injection,
		e.Outcome,
		e.CredentialRef,
		e.CredentialScope,
		e.Error,
	)
	if err != nil {
		return fmt.Errorf("audit: record: %w", err)
	}
	return nil
}

// checkInvariant enforces:
//
//  1. credential_ref IS NOT NULL ⟺ injection = 'applied'.
//  2. credential_ref IS NOT NULL ⟺ credential_scope IS NOT NULL
//     (CredentialScope and CredentialRef must agree: both set or both nil).
func checkInvariant(e Entry) error {
	injApplied := e.Injection != nil && *e.Injection == "applied"
	hasRef := e.CredentialRef != nil

	if injApplied != hasRef {
		return ErrInvariantViolation
	}

	if (e.CredentialRef != nil) != (e.CredentialScope != nil) {
		return errors.New("audit: CredentialScope and CredentialRef must agree (both set or both nil)")
	}

	return nil
}

// Query returns entries matching filter in descending timestamp order.
func (l *sqlLogger) Query(ctx context.Context, f Filter) ([]Entry, error) {
	var (
		conds []string
		args  []any
	)

	if f.After != nil {
		conds = append(conds, "ts > ?")
		args = append(args, f.After.UnixMilli())
	}
	if f.Before != nil {
		conds = append(conds, "ts < ?")
		args = append(args, f.Before.UnixMilli())
	}
	if f.Agent != nil {
		conds = append(conds, "agent = ?")
		args = append(args, *f.Agent)
	}
	if f.Host != nil {
		conds = append(conds, "host = ?")
		args = append(args, *f.Host)
	}
	if f.Rule != nil {
		conds = append(conds, "matched_rule = ?")
		args = append(args, *f.Rule)
	}

	q := `SELECT id, ts, agent, interception, method, host, path, query,
	             status, duration_ms, bytes_in, bytes_out,
	             matched_rule, rule_verdict, approval, injection, outcome,
	             credential_ref, credential_scope, error
	      FROM requests`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY ts DESC"

	if f.Limit != nil {
		q += fmt.Sprintf(" LIMIT %d", *f.Limit)
	}
	if f.Offset != nil {
		if f.Limit == nil {
			// OFFSET requires LIMIT in SQLite; use -1 for "all rows".
			q += " LIMIT -1"
		}
		q += fmt.Sprintf(" OFFSET %d", *f.Offset)
	}

	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var tsUnix int64
		var (
			agent           sql.NullString
			method          sql.NullString
			path            sql.NullString
			query           sql.NullString
			status          sql.NullInt64
			matchedRule     sql.NullString
			ruleVerdict     sql.NullString
			approval        sql.NullString
			injection       sql.NullString
			credentialRef   sql.NullString
			credentialScope sql.NullString
			errStr          sql.NullString
		)
		if err := rows.Scan(
			&e.ID,
			&tsUnix,
			&agent,
			&e.Interception,
			&method,
			&e.Host,
			&path,
			&query,
			&status,
			&e.DurationMS,
			&e.BytesIn,
			&e.BytesOut,
			&matchedRule,
			&ruleVerdict,
			&approval,
			&injection,
			&e.Outcome,
			&credentialRef,
			&credentialScope,
			&errStr,
		); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		e.TS = time.UnixMilli(tsUnix).UTC()
		if agent.Valid {
			e.Agent = &agent.String
		}
		if method.Valid {
			e.Method = &method.String
		}
		if path.Valid {
			e.Path = &path.String
		}
		if query.Valid {
			e.Query = &query.String
		}
		if status.Valid {
			v := int(status.Int64)
			e.Status = &v
		}
		if matchedRule.Valid {
			e.MatchedRule = &matchedRule.String
		}
		if ruleVerdict.Valid {
			e.RuleVerdict = &ruleVerdict.String
		}
		if approval.Valid {
			e.Approval = &approval.String
		}
		if injection.Valid {
			e.Injection = &injection.String
		}
		if credentialRef.Valid {
			e.CredentialRef = &credentialRef.String
		}
		if credentialScope.Valid {
			e.CredentialScope = &credentialScope.String
		}
		if errStr.Valid {
			e.Error = &errStr.String
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: rows: %w", err)
	}
	return entries, nil
}

// Prune deletes all rows with ts strictly less than before (WHERE ts < before).
// It returns the count of deleted rows.
func (l *sqlLogger) Prune(ctx context.Context, before time.Time) (int, error) {
	res, err := l.db.ExecContext(ctx,
		"DELETE FROM requests WHERE ts < ?",
		before.UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("audit: prune rows affected: %w", err)
	}
	return int(n), nil
}
