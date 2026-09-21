package invocation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// TrafficHistory carries explicit generation/pruning evidence. Any pruning
// invalidates a prior snapshot, including non-prefix holes around active pins.
// These values are history metadata, never receipts or dispatch authority.
type TrafficHistory struct {
	Generation string
	HighWater  int64
	Pruning    int64
	Records    []contract.InvocationAuditRecord
}

func (s *TrafficStore) History(ctx context.Context, after int64, limit int) (result TrafficHistory, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrTrafficCapacity) &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.mu.Lock()
			s.faulted = true
			s.mu.Unlock()
		}
	}()
	if after < 0 || limit < 1 || limit > 256 {
		return result, ErrInvalidInput
	}
	select {
	case s.readSlots <- struct{}{}:
		defer func() { <-s.readSlots }()
	default:
		return result, ErrTrafficCapacity
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.ReadLifetime)
	defer cancel()
	// Try rather than queue behind checkpoint maintenance. Readers never leak a
	// live SQLite snapshot to callers or prolong the writer's pressure window.
	if !s.readGate.TryRLock() {
		return result, ErrTrafficCapacity
	}
	defer s.readGate.RUnlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return result, ErrTrafficFault
	}
	tx, err := s.readerDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&result.Generation, &result.HighWater, &result.Pruning); err != nil {
		return TrafficHistory{}, err
	}
	rows, err := tx.QueryContext(ctx, invocationSelect+` WHERE insertion_sequence>? ORDER BY insertion_sequence LIMIT ?`, after, limit)
	if err != nil {
		return TrafficHistory{}, err
	}
	for rows.Next() {
		record, scanErr := scanInvocation(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		if !validStoredInvocation(record) {
			err = ErrInvalidState
			break
		}
		result.Records = append(result.Records, record)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return TrafficHistory{}, err
	}
	if err = tx.Commit(); err != nil {
		return TrafficHistory{}, err
	}
	return result, nil
}
