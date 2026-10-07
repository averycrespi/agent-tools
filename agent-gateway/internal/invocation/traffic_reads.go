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
	if after < 0 || limit < 1 || limit > 256 {
		return result, ErrInvalidInput
	}
	err = s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT generation,high_water,pruning FROM traffic_meta WHERE singleton=1`).Scan(&result.Generation, &result.HighWater, &result.Pruning); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, invocationSelect+` WHERE insertion_sequence>? ORDER BY insertion_sequence LIMIT ?`, after, limit)
		if err != nil {
			return err
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
		return errors.Join(err, rows.Err(), rows.Close())
	})
	if err != nil {
		return TrafficHistory{}, err
	}
	return result, nil
}
