package invocation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// These builders create historical single-store rows for migration and reader
// qualification. They are not compiled into the runtime execution path.
func NewRepository(store *storage.Store, clock Clock, entropy io.Reader, invalidators ...func(contract.Invalidation)) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil || len(invalidators) > 1 || len(invalidators) == 1 && invalidators[0] == nil {
		return nil, ErrInvalidInput
	}
	r := &Repository{store: store, clock: clock, entropy: entropy, limit: invocationLimit()}
	if len(invalidators) == 1 {
		r.invalidate = invalidators[0]
	}
	if _, err := rand.Read(r.cursorKey[:]); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *Repository) mutate(ctx context.Context, mutate func(*sql.Tx) error) error {
	err := r.store.Mutate(ctx, mutate)
	if err == nil || isInvocationError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}
func (r *Repository) Insert(ctx context.Context, p PreparedAdmission) error {
	if err := r.mutate(ctx, func(tx *sql.Tx) error { return r.InsertTx(ctx, tx, p) }); err != nil {
		return err
	}
	r.publish(p.InvocationID)
	return nil
}
func (r *Repository) InsertTx(ctx context.Context, tx *sql.Tx, p PreparedAdmission) error {
	if tx == nil || !validPreparedAdmission(p) {
		return ErrInvalidInput
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM invocations WHERE id=?)`, p.InvocationID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return ErrIdentityUnavailable
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count); err != nil {
		return err
	}
	if excess := count - r.limit + 1; excess > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM invocations WHERE insertion_sequence IN (SELECT insertion_sequence FROM invocations ORDER BY insertion_sequence LIMIT ?)`, excess); err != nil {
			return err
		}
	}
	values, err := admissionSQLValues(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO invocations (id,principal_id,credential_id,credential_fingerprint,credential_revision,admitted_at,admission_class,requested_name,redacted_arguments,server_id,tool_id,upstream_name,descriptor_revision,descriptor_fingerprint,decision,authorization_revision,evaluated_at,grant_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...)
	return err
}
func (r *Repository) AnnotateTerminal(ctx context.Context, id string, terminal contract.InvocationTerminalClass) error {
	return r.annotateTerminal(ctx, id, terminal, nil)
}
func (r *Repository) annotateTerminal(ctx context.Context, id string, terminal contract.InvocationTerminalClass, diagnostic *contract.FailureDiagnostics) error {
	encoded, err := encodeFailureDiagnostics(terminal, diagnostic)
	if err != nil {
		return err
	}
	if !validOpaqueInvocationID(id) {
		return ErrInvalidInput
	}
	if _, err := contract.ParseInvocationTerminalClass(string(terminal)); err != nil {
		return ErrInvalidInput
	}
	at, ok := canonicalInvocationTimestamp(r.clock.Now())
	if !ok {
		return ErrInvalidInput
	}
	changed := false
	err = r.mutate(ctx, func(tx *sql.Tx) error {
		var admitted, evaluated string
		err := tx.QueryRowContext(ctx, `SELECT admitted_at,evaluated_at FROM invocations WHERE id=? AND admission_class='evaluated' AND decision='allow' AND completed_at IS NULL AND terminal_class IS NULL`, id).Scan(&admitted, &evaluated)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		a, av := parseCanonicalInvocationTimestamp(admitted)
		e, ev := parseCanonicalInvocationTimestamp(evaluated)
		c, cv := parseCanonicalInvocationTimestamp(at)
		if !av || !ev || !cv || c.Before(a) || c.Before(e) {
			return ErrInvalidInput
		}
		result, err := tx.ExecContext(ctx, `UPDATE invocations SET completed_at=?,terminal_class=?,failure_diagnostics=? WHERE id=? AND completed_at IS NULL AND terminal_class IS NULL`, at, string(terminal), encoded, id)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		changed = rows == 1
		return err
	})
	if err == nil && changed {
		r.publish(id)
	}
	return err
}
