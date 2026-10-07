package invocation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ncruces/go-sqlite3"
)

var errTrafficReadLifetime = errors.New("traffic read lifetime exceeded")

// Filter individual error branches, not the joined result: cancellation or a
// benign domain result must never hide independent storage/settlement evidence.
func trafficReadFault(err, callerErr error) error {
	if err == nil {
		return nil
	}
	//nolint:errorlint // Inspect only this node; joined siblings are filtered independently below.
	if _, ok := err.(*trafficFailure); ok {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var faults []error
		for _, child := range joined.Unwrap() {
			faults = append(faults, trafficReadFault(child, callerErr))
		}
		return errors.Join(faults...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return trafficReadFault(wrapped.Unwrap(), callerErr)
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidCursor) || errors.Is(err, ErrStaleCursor) ||
		errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) ||
		errors.Is(err, ErrTrafficCapacity) || errors.Is(err, ErrTrafficFault) {
		return nil
	}
	if callerErr != nil && (errors.Is(err, callerErr) || errors.Is(err, sqlite3.INTERRUPT)) {
		return nil
	}
	return err
}

// view materializes one bounded response. No transaction, rows or reader lease
// escapes to clients; checkpoint/close fences readers through the same gate.
func (s *TrafficStore) view(ctx context.Context, read func(context.Context, *sql.Tx) error) (result error) {
	if s.optional != nil {
		target := s.optionalTarget()
		if target == nil {
			return ErrTrafficFault
		}
		return target.view(ctx, read)
	}
	caller := ctx
	defer func() {
		callerErr := caller.Err()
		if errors.Is(context.Cause(ctx), errTrafficReadLifetime) {
			callerErr = nil
		}
		if fault := trafficReadFault(result, callerErr); fault != nil {
			s.failTraffic(fault, "read", "not_started")
		}
	}()
	select {
	case s.readSlots <- struct{}{}:
		defer func() { <-s.readSlots }()
	default:
		return ErrTrafficCapacity
	}
	if !s.readGate.TryRLock() {
		return ErrTrafficCapacity
	}
	defer s.readGate.RUnlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrTrafficFault
	}
	ctx, cancel := context.WithTimeoutCause(ctx, s.config.ReadLifetime, errTrafficReadLifetime)
	defer cancel()
	conn, err := s.readerDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, conn.Close()) }()
	// Keep settlement synchronous so its error cannot be discarded by
	// database/sql's cancellation goroutine. Every read still gets ctx's bound.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	err = read(ctx, tx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		rollback := tx.Rollback()
		if rollback != nil {
			return errors.Join(err, classifyTraffic(rollback, "rollback", "uncertain"))
		}
		return err
	}
	if err = tx.Commit(); err != nil {
		return classifyTraffic(err, "commit", "uncertain")
	}
	return nil
}
