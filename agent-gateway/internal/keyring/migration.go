package keyring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrMigrationIncomplete = errors.New("secret migration is incomplete; inspect remaining dependencies before cleanup")

// MigrationResult is deliberately bounded and contains no native identifiers or errors.
type MigrationResult struct {
	Migrated       int  `json:"migrated"`
	Remaining      int  `json:"remaining"`
	RemainingKnown bool `json:"remaining_known"`
	Deleted        int  `json:"deleted_items"`
	Uncertain      int  `json:"uncertain_items"`
	Retained       int  `json:"retained_items"`
}

type migrationRecord struct {
	handle   Handle
	owner    string
	kind     RecordKind
	revision int64
}

// MigrateLegacy retains native sources and changes only an exact current custody
// selector. The caller owns exclusive stopped installation access throughout.
func MigrateLegacy(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, native *Provider, clock Clock, inspect func(context.Context, *sql.Tx, RecordKind, Handle, []byte) error) (MigrationResult, error) {
	return migrateLegacy(ctx, owner, store, native, clock, inspect, nil)
}

func migrateLegacy(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, native *Provider, clock Clock, inspect func(context.Context, *sql.Tx, RecordKind, Handle, []byte) error, fault func(string) error) (result MigrationResult, err error) {
	if native == nil || native.custody != nil || inspect == nil || store == nil {
		return result, ErrCustodyUnavailable
	}
	defer func() {
		var remaining int
		countErr := store.View(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM secret_generations WHERE custody='legacy') + (SELECT count(*) FROM keyring_authorities a LEFT JOIN secret_generations g ON g.handle=a.handle AND g.owner=a.owner AND g.kind=a.kind WHERE g.handle IS NULL)`).Scan(&remaining)
		})
		result.RemainingKnown = countErr == nil
		result.Remaining = remaining
		if countErr != nil || remaining != 0 {
			err = errors.Join(err, ErrMigrationIncomplete)
		}
	}()
	if err = SetupCustody(ctx, owner, store, clock); err != nil {
		return result, err
	}
	encrypted, err := NewProviderWithBackend(native.installationID, native.adapter)
	if err != nil {
		return result, err
	}
	if err = encrypted.UseDatabaseCustody(ctx, owner, store); err != nil {
		return result, err
	}
	after := ""
	for {
		var record migrationRecord
		err = store.View(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT a.handle,a.owner,a.kind,a.revision FROM keyring_authorities a JOIN secret_generations g ON g.handle=a.handle AND g.owner=a.owner AND g.kind=a.kind WHERE g.custody='legacy' AND a.handle>? ORDER BY a.handle LIMIT 1`, after).Scan(&record.handle, &record.owner, &record.kind, &record.revision)
		})
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return result, err
		}
		after = string(record.handle)
		ns, nsErr := NewNamespace(native.installationID, record.owner, record.kind)
		if nsErr != nil {
			return result, ErrMigrationIncomplete
		}
		// No native work is performed for a fenced, changed or deleted authority.
		if err = store.View(ctx, func(tx *sql.Tx) error { return migrationCurrent(ctx, tx, record) }); err != nil {
			result.Remaining++
			continue
		}
		payload, readErr := native.ReadGeneration(ctx, ns, record.handle)
		if readErr != nil {
			result.Remaining++
			continue
		}
		copyErr := migrateRecord(ctx, store, encrypted.custody, record, ns, payload, clock, inspect, fault)
		clear(payload)
		if copyErr != nil {
			return result, copyErr
		}
		result.Migrated++
	}
	if result.Remaining != 0 {
		return result, ErrMigrationIncomplete
	}
	return result, nil
}

func migrationCurrent(ctx context.Context, tx *sql.Tx, r migrationRecord) error {
	var current int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities a JOIN secret_generations g ON g.handle=a.handle AND g.owner=a.owner AND g.kind=a.kind WHERE a.owner=? AND a.kind=? AND a.handle=? AND a.revision=? AND g.custody='legacy' AND NOT EXISTS(SELECT 1 FROM keyring_authority_fences f WHERE f.owner=a.owner AND f.kind=a.kind) AND NOT EXISTS(SELECT 1 FROM keyring_candidates c WHERE c.owner=a.owner AND c.kind=a.kind)`, r.owner, r.kind, string(r.handle), r.revision).Scan(&current)
	if err != nil {
		return err
	}
	if current != 1 {
		return ErrMigrationIncomplete
	}
	return nil
}

func migrateRecord(ctx context.Context, store *storage.Store, custody *databaseCustody, r migrationRecord, ns Namespace, payload []byte, clock Clock, inspect func(context.Context, *sql.Tx, RecordKind, Handle, []byte) error, fault func(string) error) (resultErr error) {
	effect := ""
	defer func() {
		if resultErr != nil && effect != "" {
			resultErr = &storage.OperationError{Effect: effect, Cause: resultErr}
		}
	}()
	step := func(s string) error {
		if fault != nil {
			return fault(s)
		}
		return nil
	}
	var budget BackupCustody
	var revision int64
	if err := store.View(ctx, func(tx *sql.Tx) error {
		if err := migrationCurrent(ctx, tx, r); err != nil {
			return err
		}
		if err := inspect(ctx, tx, r.kind, r.handle, payload); err != nil {
			return err
		}
		var err error
		budget, err = RestoreHighWaterTx(ctx, tx)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT revision FROM gateway_meta WHERE singleton=1`).Scan(&revision)
	}); err != nil {
		return err
	}
	// Reserve before encryption. Failed/rolled-back copies never refund this nonce.
	if budget.KeyID != custody.keyID {
		return ErrCustodyUnavailable
	}
	if _, err := ReserveRecoveryEncryptions(ctx, store, budget, 1); err != nil {
		return err
	}
	effect = "reserved"
	if err := step("reserved"); err != nil {
		return err
	}
	sealed := custody.aead.Seal(nil, nil, payload, custodyBinding(ns, r.handle, custody.keyID))
	verified, err := custody.aead.Open(nil, nil, sealed, custodyBinding(ns, r.handle, custody.keyID))
	same := bytes.Equal(payload, verified)
	clear(verified)
	if err != nil || !same {
		return ErrIncompleteGeneration
	}
	if err := step("copied"); err != nil {
		return err
	}
	effect = "uncertain"
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := migrationCurrent(ctx, tx, r); err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM gateway_meta WHERE singleton=1`).Scan(&current); err != nil {
			return err
		}
		if current != revision+1 {
			return ErrMigrationIncomplete
		}
		if err := inspect(ctx, tx, r.kind, r.handle, payload); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE secret_generations SET custody='encrypted',version=1,key_id=?,ciphertext=? WHERE handle=? AND custody='legacy'`, custody.keyID, sealed, string(r.handle)); err != nil {
			return err
		}
		chunks := (len(payload) + rawChunkMaximumBytes - 1) / rawChunkMaximumBytes
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_cleanup(handle,owner,kind,revision,chunks) VALUES(?,?,?,?,?)`, string(r.handle), r.owner, r.kind, r.revision, chunks); err != nil {
			return err
		}
		// Include every bounded slot owned by this generation, so interrupted native
		// writes and residual chunks remain detectable even after manifest deletion.
		for item := -1; item < maximumGenerationChunks; item++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO native_cleanup_items(handle,item,state) VALUES(?,?,'retained')`, string(r.handle), item); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`); err != nil {
			return err
		}
		if err := audit.MutationTx(audit.WithOffline(ctx), tx, clock.Now(), "keyring", "commit", authorityAuditTarget(ns)); err != nil {
			return err
		}
		return step("before_commit")
	})
	if err != nil {
		return err
	}
	return step("committed")
}
