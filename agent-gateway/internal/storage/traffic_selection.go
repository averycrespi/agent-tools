package storage

import (
	"context"
	"database/sql"
	"errors"
)

var ErrTrafficUnselected = errors.New("traffic generation is not selected; stopped traffic migration is required")

// SelectedTraffic reads only control-owned selection metadata. An empty selection
// is a legitimate migrated legacy control store, but is never ready to serve.
func (store *Store) SelectedTraffic(ctx context.Context) (string, error) {
	var generation string
	err := store.View(ctx, func(tx *sql.Tx) error {
		var err error
		generation, err = selectedTraffic(ctx, tx)
		return err
	})
	return generation, err
}

// Both online snapshots and immutable backup reads enforce the same pair invariant.
func selectedTraffic(ctx context.Context, reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, error) {
	var generation sql.NullString
	err := reader.QueryRowContext(ctx, `SELECT generation FROM traffic_selection WHERE singleton=1`).Scan(&generation)
	if err != nil {
		return "", err
	}
	if generation.Valid {
		if !installationIDPattern.MatchString(generation.String) {
			return "", ErrInvalidDatabase
		}
		var count int
		if err := reader.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count); err != nil {
			return "", err
		}
		if count != 0 {
			return "", ErrInvalidDatabase
		}
	}
	return generation.String, nil
}

// SelectTraffic is called only by stopped migration/replacement orchestration,
// after publishing and validating the generation. The control commit is the
// single pair-selection point; it never renames two active fixed paths.
func (store *Store) SelectTraffic(ctx context.Context, expected, generation string) error {
	if !installationIDPattern.MatchString(generation) || expected != "" && !installationIDPattern.MatchString(expected) {
		return ErrInvalidDatabase
	}
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE traffic_selection SET generation=? WHERE singleton=1 AND COALESCE(generation,'')=?`, generation, expected)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrInvalidDatabase
		}
		return nil
	})
}
