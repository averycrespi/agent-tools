package invocation

import (
	"context"
	"database/sql"
	"errors"
)

// view materializes one bounded response. No transaction, rows or reader lease
// escapes to clients; checkpoint/close fences readers through the same gate.
func (s *TrafficStore) view(ctx context.Context, read func(*sql.Tx) error) error {
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
	ctx, cancel := context.WithTimeout(ctx, s.config.ReadLifetime)
	defer cancel()
	tx, err := s.readerDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	err = read(tx)
	if err != nil {
		rollback := tx.Rollback()
		if errors.Is(rollback, sql.ErrTxDone) {
			rollback = nil
		}
		return errors.Join(err, rollback)
	}
	return tx.Commit()
}
