package storage

import (
	"context"
	"database/sql"
	"errors"
)

// ErrLegacyCustody never licenses repair or deletion by this binary. Preserve the
// selected installation and use the pre-removal upgrade procedure instead.
var ErrLegacyCustody = errors.New("unresolved legacy secret custody; preserve this installation and complete the pre-removal upgrade procedure before using this version")

// RequireNoLegacyCustodyTx inspects only explicit database provenance. Historical
// cleanup observations remain immutable evidence, not a native-provider work queue.
func RequireNoLegacyCustodyTx(ctx context.Context, tx *sql.Tx) error {
	var schema, unresolved int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return err
	}
	if schema < 3 {
		return nil
	}
	query := `SELECT EXISTS(SELECT 1 FROM keyring_authorities) OR EXISTS(SELECT 1 FROM keyring_candidates)`
	if schema >= 23 {
		query = `SELECT EXISTS(SELECT 1 FROM secret_generations WHERE custody='legacy')`
	}
	if err := tx.QueryRowContext(ctx, query).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved != 0 {
		return ErrLegacyCustody
	}
	if schema >= 24 {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_cleanup_items WHERE state<>'deleted') OR EXISTS(SELECT 1 FROM native_cleanup n WHERE (SELECT count(*) FROM native_cleanup_items i WHERE i.handle=n.handle)<>118)`).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved != 0 {
			return ErrLegacyCustody
		}
	}
	return nil
}
