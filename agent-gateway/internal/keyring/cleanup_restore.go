package keyring

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// Schema 24 permanently records 117 chunk slots and one manifest observation.
const maximumGenerationChunks = 117

type migrationRecord struct {
	handle   Handle
	owner    string
	kind     RecordKind
	revision int64
}

var ErrMigrationIncomplete = errors.New("retained native cleanup provenance is incomplete; preserve the installation and consult the pre-removal upgrade procedure")

// PreserveCleanupInventory carries forward irreversible native observations from
// the stopped current database into a private restore stage. Backup state can
// add evidence but cannot erase newer ownership or weaken deletion dispositions.
func PreserveCleanupInventory(ctx context.Context, current *sql.Tx, replacement *storage.Store) error {
	var schema int
	if err := current.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return err
	}
	if schema < 24 {
		return nil
	}
	var installation string
	if err := current.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installation); err != nil {
		return err
	}
	identity, err := replacement.Identity(ctx)
	if err != nil || identity.InstallationID != installation {
		return ErrMigrationIncomplete
	}
	after := ""
	for {
		var r migrationRecord
		var chunks int
		err := current.QueryRowContext(ctx, `SELECT handle,owner,kind,revision,chunks FROM native_cleanup WHERE handle>? ORDER BY handle LIMIT 1`, after).Scan(&r.handle, &r.owner, &r.kind, &r.revision, &chunks)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := ParseHandle(string(r.handle)); err != nil {
			return ErrMigrationIncomplete
		}
		if _, err := NewNamespace(installation, r.owner, r.kind); err != nil {
			return ErrMigrationIncomplete
		}
		states := make([]string, maximumGenerationChunks+1)
		for item := -1; item < maximumGenerationChunks; item++ {
			if err := current.QueryRowContext(ctx, `SELECT state FROM native_cleanup_items WHERE handle=? AND item=?`, string(r.handle), item).Scan(&states[item+1]); err != nil {
				return err
			}
			if cleanupStateRank(states[item+1]) < 0 {
				return ErrMigrationIncomplete
			}
		}
		if err := replacement.Mutate(ctx, func(tx *sql.Tx) error {
			var existing migrationRecord
			var existingChunks int
			err := tx.QueryRowContext(ctx, `SELECT owner,kind,revision,chunks FROM native_cleanup WHERE handle=?`, string(r.handle)).Scan(&existing.owner, &existing.kind, &existing.revision, &existingChunks)
			if err == nil && (existing.owner != r.owner || existing.kind != r.kind || existing.revision != r.revision || existingChunks != chunks) {
				return ErrMigrationIncomplete
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) {
				if _, err := tx.ExecContext(ctx, `INSERT INTO native_cleanup(handle,owner,kind,revision,chunks) VALUES(?,?,?,?,?)`, string(r.handle), r.owner, r.kind, r.revision, chunks); err != nil {
					return err
				}
			}
			for item := -1; item < maximumGenerationChunks; item++ {
				state := states[item+1]
				var previous string
				err := tx.QueryRowContext(ctx, `SELECT state FROM native_cleanup_items WHERE handle=? AND item=?`, string(r.handle), item).Scan(&previous)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					if cleanupStateRank(previous) < 0 {
						return ErrMigrationIncomplete
					}
					if cleanupStateRank(previous) > cleanupStateRank(state) {
						state = previous
					}
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO native_cleanup_items(handle,item,state) VALUES(?,?,?) ON CONFLICT(handle,item) DO UPDATE SET state=excluded.state`, string(r.handle), item, state); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		after = string(r.handle)
	}
}

func cleanupStateRank(state string) int {
	switch state {
	case "retained":
		return 0
	case "uncertain":
		return 1
	case "deleted":
		return 2
	case "reappeared":
		return 3
	default:
		return -1
	}
}
