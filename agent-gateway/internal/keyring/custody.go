package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrCustodyUnavailable = errors.New("encrypted secret custody is unavailable; inspect the installation master key and stopped setup")

type databaseCustody struct {
	store *storage.Store
	keyID string
	aead  cipher.AEAD
}

// SetupCustody is a stopped, explicitly approved operation. The key is published
// durably before its identity is committed. An interrupted complete key can be
// adopted, but no existing file or established database key identity is replaced.
func SetupCustody(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, clock Clock) error {
	if clock == nil || owner == nil || store == nil {
		return ErrCustodyUnavailable
	}
	id, err := custodyKeyID(ctx, store)
	if err != nil {
		return err
	}
	if id == "" {
		var count int
		if err = store.View(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secret_generations WHERE custody = 'encrypted')`).Scan(&count)
		}); err != nil || count != 0 {
			return ErrCustodyUnavailable
		}
	}
	key, err := gatewaypaths.DurableMasterKey(owner)
	if errors.Is(err, os.ErrNotExist) && id == "" {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return ErrCustodyUnavailable
		}
		defer clear(key)
		key, err = gatewaypaths.MasterKey(owner, key)
	}
	if err != nil {
		return ErrCustodyUnavailable
	}
	defer clear(key)
	actual := masterKeyID(key)
	if id != "" {
		if id != actual {
			return ErrCustodyUnavailable
		}
		return nil
	}
	return store.Mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO secret_custody (singleton, key_id, version) VALUES (1, ?, 1)`, actual); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`); err != nil {
			return err
		}
		var installation string
		if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton = 1`).Scan(&installation); err != nil {
			return err
		}
		return audit.MutationTx(audit.WithOffline(ctx), tx, clock.Now(), "keyring", "setup", contract.AuditTarget{Type: "installation", ID: installation})
	})
}

// DatabaseCustodyTx reads only safe custody selection, including historical schemas.
func DatabaseCustodyTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var schema, count int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return false, err
	}
	if schema < 23 {
		return false, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secret_custody) OR EXISTS(SELECT 1 FROM secret_generations WHERE custody = 'encrypted')`).Scan(&count)
	return count != 0, err
}

func custodyKeyID(ctx context.Context, store *storage.Store) (string, error) {
	var id string
	err := store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT key_id FROM secret_custody WHERE singleton = 1 AND version = 1`).Scan(&id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func masterKeyID(key []byte) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}

// UseDatabaseCustody is called only during composition, before publication.
// An unconfigured legacy installation can read explicit legacy generations, but
// cannot write secrets until stopped setup. Missing/wrong established keys refuse.
func (provider *Provider) UseDatabaseCustody(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store) error {
	if owner == nil || store == nil {
		return ErrCustodyUnavailable
	}
	identity, err := store.Identity(ctx)
	if err != nil || identity.InstallationID != provider.installationID {
		return ErrCustodyUnavailable
	}
	id, err := custodyKeyID(ctx, store)
	if err != nil {
		return ErrCustodyUnavailable
	}
	custody := &databaseCustody{store: store, keyID: id}
	if id == "" {
		var encrypted bool
		if err := store.View(ctx, func(tx *sql.Tx) error {
			var err error
			encrypted, err = DatabaseCustodyTx(ctx, tx)
			return err
		}); err != nil || encrypted {
			return ErrCustodyUnavailable
		}
	}
	if id != "" {
		key, err := gatewaypaths.MasterKey(owner, nil)
		if err != nil {
			return ErrCustodyUnavailable
		}
		defer clear(key)
		if masterKeyID(key) != id {
			return ErrCustodyUnavailable
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return ErrCustodyUnavailable
		}
		custody.aead, err = cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return ErrCustodyUnavailable
		}
	}
	provider.custody = custody
	return nil
}

func custodyBinding(namespace Namespace, handle Handle, keyID string) []byte {
	// All fields have closed alphabets; NUL makes their boundaries unambiguous.
	return []byte("agent-gateway/secret/v1\x00" + namespace.installationID + "\x00" + namespace.owner + "\x00" + string(namespace.kind) + "\x00" + string(handle) + "\x00" + keyID)
}

func (custody *databaseCustody) write(ctx context.Context, namespace Namespace, handle Handle, secret []byte) error {
	if custody.aead == nil {
		return ErrCustodyUnavailable
	}
	sealed := custody.aead.Seal(nil, nil, secret, custodyBinding(namespace, handle, custody.keyID))
	return custody.store.Mutate(ctx, func(tx *sql.Tx) error {
		// GCM random nonces permit at most 2^32 exposed encryptions per key.
		// Charge durable ciphertext publication, including later-retired records.
		result, err := tx.ExecContext(ctx, `UPDATE secret_custody SET encryptions = encryptions + 1 WHERE singleton = 1 AND key_id = ? AND encryptions < 4294967296`, custody.keyID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrCustodyUnavailable
		}
		// This insertion cannot overwrite an existing generation.
		_, err = tx.ExecContext(ctx, `INSERT INTO secret_generations (handle, owner, kind, custody, version, key_id, ciphertext) VALUES (?, ?, ?, 'encrypted', 1, ?, ?)`, string(handle), namespace.owner, namespace.kind, custody.keyID, sealed)
		return err
	})
}

func (custody *databaseCustody) read(ctx context.Context, namespace Namespace, handle Handle) ([]byte, bool, error) {
	var origin string
	var version sql.NullInt64
	var keyID sql.NullString
	var sealed []byte
	err := custody.store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT custody, version, key_id, substr(ciphertext, 1, 262173) FROM secret_generations WHERE handle = ? AND owner = ? AND kind = ?`, string(handle), namespace.owner, namespace.kind).Scan(&origin, &version, &keyID, &sealed)
	})
	if err != nil {
		return nil, false, ErrIncompleteGeneration
	}
	if origin == "legacy" && !version.Valid && !keyID.Valid && sealed == nil {
		return nil, true, nil
	}
	if origin != "encrypted" || !version.Valid || version.Int64 != 1 || !keyID.Valid || keyID.String != custody.keyID || custody.aead == nil || len(sealed) < 29 || len(sealed) > secretMaximumBytes+28 {
		return nil, false, ErrIncompleteGeneration
	}
	secret, err := custody.aead.Open(nil, nil, sealed, custodyBinding(namespace, handle, custody.keyID))
	if err != nil {
		return nil, false, ErrIncompleteGeneration
	}
	return secret, false, nil
}

func (custody *databaseCustody) remove(ctx context.Context, namespace Namespace, handle Handle) error {
	// Legacy native objects are deliberately retained for the separately approved
	// migration/cutover. Removing their explicit selector cannot resurrect them.
	return custody.store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM secret_generations WHERE handle = ? AND owner = ? AND kind = ? AND NOT EXISTS (SELECT 1 FROM keyring_authorities WHERE handle = ?)`, string(handle), namespace.owner, namespace.kind, string(handle))
		return err
	})
}

func (custody *databaseCustody) capability() Capability {
	if custody.aead != nil {
		return Capability{State: contract.KeyringReady, Remediation: RemediationNone}
	}
	return Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}
}
