package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// OpenReplacement opens and fully verifies a staged database generation under stopped-process ownership.
func OpenReplacement(ctx context.Context, ownership *gatewaypaths.Ownership, path string) (*Store, error) {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	if filepath.Dir(path) != layout.Root {
		return nil, fmt.Errorf("%w: replacement is outside the installation root", ErrInvalidDatabase)
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
		return nil, fmt.Errorf("%w: replacement path: %w", ErrInvalidDatabase, err)
	}
	version, err := inspectDatabase(ctx, path)
	if err != nil {
		return nil, err
	}
	if version < 3 || version > CurrentSchema {
		return nil, fmt.Errorf("%w: replacement schema %d is not supported", ErrInvalidDatabase, version)
	}
	layout.Database = path
	layout.MutationMarker = path + ".mutation"
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return nil, err
	}
	if err := store.View(ctx, func(tx *sql.Tx) error { return RequireNoLegacyCustodyTx(ctx, tx) }); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.configureSizeLimit(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.migrate(ctx, version); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.verify(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// Checkpoint closes the replacement's WAL into its database file before generation publication.
func (store *Store) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := store.database.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint replacement database: %w", err)
	}
	if busy != 0 || logFrames != checkpointed {
		return fmt.Errorf("checkpoint replacement database remained busy")
	}
	return nil
}
