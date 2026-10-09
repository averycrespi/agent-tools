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
	"fmt"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrCustodyUnavailable = errors.New("encrypted secret custody is unavailable")

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
	if err := store.View(ctx, func(tx *sql.Tx) error { return storage.RequireNoLegacyCustodyTx(ctx, tx) }); err != nil {
		return err
	}
	id, err := custodyKeyID(ctx, store)
	if err != nil {
		return err
	}
	if id == "" {
		var count int
		if err = store.View(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secret_generations) OR EXISTS(SELECT 1 FROM keyring_authorities) OR EXISTS(SELECT 1 FROM keyring_candidates)`).Scan(&count)
		}); err != nil || count != 0 {
			return custodyError(ErrCustodyUnavailable, "inspect custody before setup", owner.Layout().Root, fmt.Sprintf("rule=empty_unconfigured_custody existing_records=%t; preserve existing artifacts", count != 0), err)
		}
	}
	key, err := gatewaypaths.DurableMasterKey(owner)
	if errors.Is(err, os.ErrNotExist) && id == "" {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return custodyError(ErrCustodyUnavailable, "generate initial master key", owner.Layout().Root, "key publication not attempted", err)
		}
		defer clear(key)
		key, err = gatewaypaths.MasterKey(owner, key)
	}
	if err != nil {
		return custodyError(ErrCustodyUnavailable, "read or publish master key", filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName), "preserve key/database artifacts; publication/durability may be unconfirmed", err)
	}
	defer clear(key)
	actual := masterKeyID(key)
	if id != "" {
		if id != actual {
			return custodyError(ErrCustodyUnavailable, "bind established master key", owner.Layout().Root, "rule=key_binding expected=matching observed=different; do not regenerate key", nil)
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
// Only established encrypted custody is supported. Legacy dependencies and
// missing/wrong established keys refuse without native access or replacement.
func (provider *Provider) UseDatabaseCustody(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store) error {
	if owner == nil || store == nil {
		return ErrCustodyUnavailable
	}
	resource := filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName)
	identity, err := store.Identity(ctx)
	if err != nil {
		return custodyError(ErrCustodyUnavailable, "read installation identity", resource, "established custody not changed; preserve installation", err)
	}
	if identity.InstallationID != provider.installationID {
		return custodyError(ErrCustodyUnavailable, "bind installation", resource, "rule=installation_binding expected=current_provider observed=different; preserve installation", nil)
	}
	id, err := custodyKeyID(ctx, store)
	if err != nil {
		return custodyError(ErrCustodyUnavailable, "read custody identity", resource, "established configuration unknown; preserve installation", err)
	}
	var hasEncrypted bool
	if err := store.View(ctx, func(tx *sql.Tx) error {
		if err := storage.RequireNoLegacyCustodyTx(ctx, tx); err != nil {
			return err
		}
		var missing bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM keyring_authorities a LEFT JOIN secret_generations g ON g.handle=a.handle AND g.owner=a.owner AND g.kind=a.kind WHERE g.handle IS NULL), EXISTS(SELECT 1 FROM secret_generations WHERE custody='encrypted')`).Scan(&missing, &hasEncrypted); err != nil {
			return err
		}
		if missing {
			return ErrIncompleteGeneration
		}
		return nil
	}); err != nil {
		return err
	}
	if id == "" {
		if hasEncrypted {
			return custodyError(ErrCustodyUnavailable, "read established custody identity", resource, "rule=custody_identity_required; encrypted generations exist; preserve artifacts for recovery; do not regenerate key", nil)
		}
		return custodyError(ErrCustodyUnavailable, "configure encrypted custody", resource, "rule=custody_identity_required; encrypted writes unconfigured; inspect stopped setup prerequisites", nil)
	}
	custody := &databaseCustody{store: store, keyID: id}
	if id != "" {
		key, err := gatewaypaths.MasterKey(owner, nil)
		if err != nil {
			return custodyError(ErrCustodyUnavailable, "read established master key", resource, "established_key=true; preserve database/key artifacts for recovery; do not regenerate key", err)
		}
		defer clear(key)
		if masterKeyID(key) != id {
			return custodyError(ErrCustodyUnavailable, "bind established master key", resource, "rule=key_binding expected=matching observed=different; preserve database/key artifacts; do not regenerate key", nil)
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
			return custodyError(ErrCustodyUnavailable, "reserve encryption", namespace.owner+"/"+string(namespace.kind), "rule=matching_key_with_remaining_encryption_budget affected_rows=0 required_rows=1 limit=4294967296; inspect custody identity and encryption budget", nil)
		}
		// This insertion cannot overwrite an existing generation.
		_, err = tx.ExecContext(ctx, `INSERT INTO secret_generations (handle, owner, kind, custody, version, key_id, ciphertext) VALUES (?, ?, ?, 'encrypted', 1, ?, ?)`, string(handle), namespace.owner, namespace.kind, custody.keyID, sealed)
		return err
	})
}

func (custody *databaseCustody) read(ctx context.Context, namespace Namespace, handle Handle) ([]byte, error) {
	var origin string
	var version sql.NullInt64
	var keyID sql.NullString
	var sealed []byte
	err := custody.store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT custody, version, key_id, substr(ciphertext, 1, 262173) FROM secret_generations WHERE handle = ? AND owner = ? AND kind = ?`, string(handle), namespace.owner, namespace.kind).Scan(&origin, &version, &keyID, &sealed)
	})
	if err != nil {
		rule := "generation read failed"
		if errors.Is(err, sql.ErrNoRows) {
			rule = "rule=generation_present observed=absent"
		}
		return nil, custodyError(ErrIncompleteGeneration, "read generation", namespace.owner+"/"+string(namespace.kind), rule, err)
	}
	if origin == "legacy" {
		return nil, storage.ErrLegacyCustody
	}
	resource := namespace.owner + "/" + string(namespace.kind)
	if origin != "encrypted" {
		return nil, custodyError(ErrIncompleteGeneration, "read generation", resource, "rule=encrypted_custody_required observed=unsupported", nil)
	}
	if !version.Valid || version.Int64 != 1 {
		return nil, custodyError(ErrIncompleteGeneration, "read generation", resource, fmt.Sprintf("rule=generation_version present=%t observed=%d expected=1", version.Valid, version.Int64), nil)
	}
	if !keyID.Valid || keyID.String != custody.keyID {
		return nil, custodyError(ErrIncompleteGeneration, "bind generation", resource, "rule=generation_key_binding expected=matching observed=absent_or_different; preserve artifacts", nil)
	}
	if custody.aead == nil {
		return nil, custodyError(ErrCustodyUnavailable, "decrypt generation", resource, "rule=encrypted_read_configuration_required; no decryption attempted", nil)
	}
	if len(sealed) < 29 || len(sealed) > secretMaximumBytes+28 {
		return nil, custodyError(ErrIncompleteGeneration, "read generation", resource, fmt.Sprintf("rule=sealed_generation_bytes observed=%d minimum=29 maximum=%d; bytes withheld", len(sealed), secretMaximumBytes+28), nil)
	}
	secret, err := custody.aead.Open(nil, nil, sealed, custodyBinding(namespace, handle, custody.keyID))
	if err != nil {
		return nil, custodyError(ErrIncompleteGeneration, "authenticate generation", resource, "rule=generation_authentication; binding/ciphertext cannot be authenticated; preserve artifacts", err)
	}
	return secret, nil
}

func (custody *databaseCustody) remove(ctx context.Context, namespace Namespace, handle Handle) error {
	// Never discard a legacy selector, including after an unexpected database change.
	return custody.store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := storage.RequireNoLegacyCustodyTx(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM secret_generations WHERE custody = 'encrypted' AND handle = ? AND owner = ? AND kind = ? AND NOT EXISTS (SELECT 1 FROM keyring_authorities WHERE handle = ?)`, string(handle), namespace.owner, namespace.kind, string(handle))
		return err
	})
}

func (custody *databaseCustody) capability() Capability {
	if custody.aead != nil {
		return Capability{State: contract.KeyringReady, Remediation: RemediationNone}
	}
	return Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}
}
