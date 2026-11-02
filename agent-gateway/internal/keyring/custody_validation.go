package keyring

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

// CustodyIdentity identifies the installation key and its nonrefundable usage.
type CustodyIdentity struct {
	KeyID       string
	Encryptions int64
}

// InspectCustodyTx accepts only complete encrypted custody. Legacy and
// unsettled generations cannot participate in stopped master-key rotation.
func InspectCustodyTx(ctx context.Context, tx *sql.Tx) (CustodyIdentity, error) {
	var result CustodyIdentity
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

// VerifyCustodyTx authenticates every retained generation, not just active
// selections. It never consults native storage.
func VerifyCustodyTx(ctx context.Context, tx *sql.Tx, owner *gatewaypaths.Ownership) (CustodyIdentity, error) {
	key, err := gatewaypaths.MasterKey(owner, nil)
	if err != nil {
		return CustodyIdentity{}, ErrCustodyUnavailable
	}
	defer clear(key)
	return VerifyKeyCustodyTx(ctx, tx, key)
}

// VerifyKeyCustodyTx authenticates all retained generations under the selected
// rotation key without selecting that key for writes.
func VerifyKeyCustodyTx(ctx context.Context, tx *sql.Tx, key []byte) (CustodyIdentity, error) {
	result, err := InspectCustodyTx(ctx, tx)
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
