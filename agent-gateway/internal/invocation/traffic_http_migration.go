package invocation

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// upgradeTraffic runs before readers, the writer and readiness exist, under the
// existing installation/process ownership. The source has already received its
// one complete validation. This transaction adds empty tables only: MCP rows,
// diagnostics, bindings, accounting and the generation remain byte-for-byte facts.
func (s *TrafficStore) upgradeTraffic(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 2 {
		return nil
	}
	if version != 1 {
		return ErrInvalidState
	}
	if err := s.reserveTraffic(ctx); err != nil {
		return err
	}
	if err := s.inject("http_migration_begin"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, storage.TrafficHTTPMigration())
	if err == nil {
		_, err = tx.ExecContext(ctx, `PRAGMA user_version=2`)
	}
	if err == nil {
		err = s.inject("http_migration_commit")
	}
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrTrafficFault, err)
	}
	if err = s.inject("http_migration_acknowledgment"); err != nil {
		return errors.Join(ErrTrafficFault, err)
	}
	// No second row scan: nothing in the validated evidence was transformed.
	if err = s.validateTrafficSchema(ctx, 2); err != nil {
		return err
	}
	return trafficFiles(s.path, s.config)
}

func (s *TrafficStore) validateHTTPTraffic(ctx context.Context, high int64) (count, charged int64, result error) {
	rows, err := s.db.QueryContext(ctx, httpTrafficSelect+` ORDER BY insertion_sequence LIMIT ?`, s.config.RetainedRecords+1)
	if err != nil {
		return 0, 0, err
	}
	var previous int64
	for rows.Next() {
		record, charge, err := scanHTTPTraffic(rows)
		if err != nil {
			result = err
			break
		}
		if count >= s.config.RetainedRecords || record.Sequence <= previous || record.Sequence > high {
			result = ErrInvalidState
			break
		}
		previous = record.Sequence
		count++
		charged += charge
	}
	result = errors.Join(result, rows.Err(), rows.Close())
	if result != nil {
		return 0, 0, result
	}
	var collision bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM http_traffic h JOIN invocations m ON m.id=h.id OR m.insertion_sequence=h.insertion_sequence)`).Scan(&collision); err != nil {
		return 0, 0, err
	}
	if collision {
		return 0, 0, ErrInvalidState
	}
	return count, charged, nil
}
