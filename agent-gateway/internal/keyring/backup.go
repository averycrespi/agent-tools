package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// BackupCustody identifies recovery material without exposing the key.
type BackupCustody struct {
	KeyID       string
	Encryptions int64
}

// VerifyRecoveryKey checks an explicit key against a previously inspected identity.
func VerifyRecoveryKey(key []byte, keyID string) error {
	if len(key) != 32 || masterKeyID(key) != keyID {
		return ErrCustodyUnavailable
	}
	return nil
}

// ReserveRecoveryEncryptions charges the active installation before any staged
// cross-key encryption. Failed stages and uncertain attempts never refund it.
func ReserveRecoveryEncryptions(ctx context.Context, store *storage.Store, expected BackupCustody, count int64) (BackupCustody, error) {
	if count < 0 || expected.Encryptions < 0 || count > 4294967296-expected.Encryptions {
		return expected, ErrCustodyUnavailable
	}
	result := BackupCustody{KeyID: expected.KeyID, Encryptions: expected.Encryptions + count}
	err := store.Mutate(ctx, func(tx *sql.Tx) error {
		changed, err := tx.ExecContext(ctx, `UPDATE secret_custody SET encryptions=? WHERE singleton=1 AND version=1 AND key_id=? AND encryptions=?`, result.Encryptions, expected.KeyID, expected.Encryptions)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrCustodyUnavailable
		}
		_, err = tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
		return err
	})
	return result, err
}

// RestoreHighWaterTx reads the established same-installation key lifetime count.
// The caller must hold stopped ownership and refuse live WAL/journal state.
func RestoreHighWaterTx(ctx context.Context, tx *sql.Tx) (BackupCustody, error) {
	var result BackupCustody
	err := tx.QueryRowContext(ctx, `SELECT key_id,encryptions FROM secret_custody WHERE singleton=1 AND version=1`).Scan(&result.KeyID, &result.Encryptions)
	if err != nil || result.Encryptions < 0 || result.Encryptions > 4294967296 {
		return result, ErrCustodyUnavailable
	}
	return result, nil
}

// InspectBackupCustodyTx accepts only complete encrypted custody. Legacy and
// unsettled generations cannot claim self-contained recovery.
func InspectBackupCustodyTx(ctx context.Context, tx *sql.Tx) (BackupCustody, error) {
	var result BackupCustody
	if err := tx.QueryRowContext(ctx, `SELECT key_id, encryptions FROM secret_custody WHERE singleton=1 AND version=1`).Scan(&result.KeyID, &result.Encryptions); err != nil {
		return result, ErrCustodyUnavailable
	}
	var invalid int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM secret_generations WHERE custody<>'encrypted' OR key_id<>?) +
 (SELECT count(*) FROM keyring_candidates) +
 (SELECT count(*) FROM keyring_authority_fences) +
 (SELECT count(*) FROM keyring_authorities a LEFT JOIN secret_generations g ON g.handle=a.handle AND g.owner=a.owner AND g.kind=a.kind WHERE g.handle IS NULL)`, result.KeyID).Scan(&invalid)
	if err != nil || invalid != 0 || result.Encryptions < 0 || result.Encryptions > 4294967296 {
		return result, ErrCustodyUnavailable
	}
	return result, nil
}

// VerifyBackupCustodyTx authenticates every retained generation, not just active
// selections. It never consults native storage. The optional domain inspector
// borrows plaintext only for semantic validation; it must not retain the bytes.
func VerifyBackupCustodyTx(ctx context.Context, tx *sql.Tx, owner *gatewaypaths.Ownership, inspect func(RecordKind, Handle, []byte) error) (BackupCustody, error) {
	key, err := gatewaypaths.MasterKey(owner, nil)
	if err != nil {
		return BackupCustody{}, ErrCustodyUnavailable
	}
	defer clear(key)
	return VerifyRecoveryCustodyTx(ctx, tx, key, inspect)
}

// VerifyRecoveryCustodyTx authenticates a closed recovery generation with an
// explicitly selected decrypt-only key. It never selects that key for writes.
func VerifyRecoveryCustodyTx(ctx context.Context, tx *sql.Tx, key []byte, inspect func(RecordKind, Handle, []byte) error) (BackupCustody, error) {
	result, err := InspectBackupCustodyTx(ctx, tx)
	if err != nil {
		return result, err
	}
	if len(key) != 32 || masterKeyID(key) != result.KeyID {
		return result, ErrCustodyUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return result, ErrCustodyUnavailable
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return result, ErrCustodyUnavailable
	}
	var installation string
	if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installation); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT handle,owner,kind,version,substr(ciphertext,1,262173) FROM secret_generations`)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	var count int64
	for rows.Next() {
		var handle, recordOwner, kind string
		var version int
		var sealed []byte
		if err := rows.Scan(&handle, &recordOwner, &kind, &version, &sealed); err != nil {
			return result, err
		}
		namespace, err := NewNamespace(installation, recordOwner, RecordKind(kind))
		if err != nil {
			return result, ErrIncompleteGeneration
		}
		parsed, err := ParseHandle(handle)
		if err != nil || version != 1 || len(sealed) < 29 || len(sealed) > secretMaximumBytes+28 {
			return result, ErrIncompleteGeneration
		}
		plaintext, err := aead.Open(nil, nil, sealed, custodyBinding(namespace, parsed, result.KeyID))
		if err == nil && inspect != nil {
			err = inspect(RecordKind(kind), parsed, plaintext)
		}
		clear(plaintext)
		if err != nil {
			return result, ErrIncompleteGeneration
		}
		count++
	}
	if count > result.Encryptions {
		return result, ErrCustodyUnavailable
	}
	return result, rows.Err()
}

// SelectedGenerationTx distinguishes active dependencies from retained ciphertext.
func SelectedGenerationTx(ctx context.Context, tx *sql.Tx, handle Handle) (bool, error) {
	var selected bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM keyring_authorities WHERE handle=?)`, string(handle)).Scan(&selected)
	return selected, err
}

// PreserveRestoreBudget carries forward the stopped installation high-water.
// Copying ciphertext consumes no nonce, but restoring must never refund usage.
func PreserveRestoreBudget(ctx context.Context, store *storage.Store, current BackupCustody) error {
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE secret_custody SET encryptions=max(encryptions,?) WHERE singleton=1 AND key_id=? AND version=1`, current.Encryptions, current.KeyID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("restore key identity changed")
		}
		return nil
	})
}
