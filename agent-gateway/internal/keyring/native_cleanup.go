package keyring

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// CleanupNative is called only by separately confirmed stopped maintenance after
// complete domain/custody verification. Inventory is retained permanently. Each
// invocation reconciles native presence before a single delete, never retries it.
func CleanupNative(ctx context.Context, store *storage.Store, native *Provider, verify func(context.Context, *sql.Tx) error) (result MigrationResult, err error) {
	if native == nil || native.custody != nil || verify == nil {
		return result, ErrCustodyUnavailable
	}
	identity, err := store.Identity(ctx)
	if err != nil || identity.InstallationID != native.installationID {
		return result, ErrCustodyUnavailable
	}
	if err = store.View(ctx, func(tx *sql.Tx) error { return verify(ctx, tx) }); err != nil {
		return result, err
	}
	after := ""
	for {
		var handle Handle
		var recordOwner string
		var kind RecordKind
		err = store.View(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT handle,owner,kind FROM native_cleanup WHERE handle>? ORDER BY handle LIMIT 1`, after).Scan(&handle, &recordOwner, &kind)
		})
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return result, err
		}
		after = string(handle)
		ns, err := NewNamespace(identity.InstallationID, recordOwner, kind)
		if err != nil {
			return result, ErrMigrationIncomplete
		}
		if _, err = ParseHandle(string(handle)); err != nil {
			return result, ErrMigrationIncomplete
		}
		if err = store.View(ctx, func(tx *sql.Tx) error {
			var unsafe int
			err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM secret_generations WHERE handle=? AND (custody<>'encrypted' OR owner<>? OR kind<>?)) + (SELECT count(*) FROM keyring_candidates WHERE handle=?) + (SELECT count(*) FROM keyring_authority_fences WHERE owner=? AND kind=?)`, string(handle), recordOwner, kind, string(handle), recordOwner, kind).Scan(&unsafe)
			if err != nil {
				return err
			}
			if unsafe != 0 {
				return ErrMigrationIncomplete
			}
			return nil
		}); err != nil {
			return result, err
		}
		for item := -1; item < maximumGenerationChunks; item++ {
			var state string
			if err = store.View(ctx, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT state FROM native_cleanup_items WHERE handle=? AND item=?`, string(handle), item).Scan(&state)
			}); err != nil {
				return result, err
			}
			name := generationManifestItem(ns, handle)
			if item >= 0 {
				name = generationChunkItem(ns, handle, item)
			}
			if err = native.validate(ns, name); err != nil {
				return result, ErrMigrationIncomplete
			}
			release, acquireErr := native.acquireWork()
			if acquireErr != nil {
				return result, acquireErr
			}
			if probeErr := native.requireReady(ctx); probeErr != nil {
				release()
				result.Uncertain++
				continue
			}
			_, readErr := native.getReady(name)
			release()
			if errors.Is(readErr, ErrNotFound) {
				if err = cleanupState(ctx, store, handle, item, "deleted"); err != nil {
					return result, err
				}
				result.Deleted++
				continue
			}
			if readErr != nil {
				result.Uncertain++
				continue
			}
			if state == "deleted" || state == "reappeared" {
				if err = cleanupState(ctx, store, handle, item, "reappeared"); err != nil {
					return result, err
				}
				result.Uncertain++
				continue
			}
			// Durable uncertainty precedes native dispatch; a lost response or crash
			// remains visible. Reappeared confirmed-deleted items are never removed.
			if err = cleanupState(ctx, store, handle, item, "uncertain"); err != nil {
				return result, err
			}
			release, acquireErr = native.acquireWork()
			if acquireErr != nil {
				return result, acquireErr
			}
			if ctx.Err() != nil {
				release()
				return result, ctx.Err()
			}
			deleteErr := native.deleteReady(name)
			_, readErr = native.getReady(name)
			release()
			if errors.Is(readErr, ErrNotFound) {
				if err = cleanupState(ctx, store, handle, item, "deleted"); err != nil {
					return result, err
				}
				result.Deleted++
			} else {
				result.Uncertain++
				if deleteErr != nil {
					result.Retained++
				}
			}
		}
	}
	if result.Uncertain != 0 {
		return result, ErrMigrationIncomplete
	}
	return result, nil
}

func cleanupState(ctx context.Context, store *storage.Store, handle Handle, item int, state string) error {
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE native_cleanup_items SET state=? WHERE handle=? AND item=? AND state<>?`, state, string(handle), item, state)
		return err
	})
}
