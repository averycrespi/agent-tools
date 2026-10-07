package httpca

import (
	"context"
	"crypto/elliptic"
	"crypto/x509"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

// VerifyBackupMaterialTx checks the selected CA key/certificate pair without
// requiring present-day validity: expiration is not artifact corruption.
func VerifyBackupMaterialTx(ctx context.Context, tx *sql.Tx, handle string, payload []byte) error {
	r, err := read(ctx, tx)
	if err != nil {
		return err
	}
	if !r.handle.Valid || r.handle.String != handle {
		return nil
	}
	var installation string
	if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM gateway_meta WHERE singleton=1`).Scan(&installation); err != nil {
		return err
	}
	var e envelope
	if strictjson.Decode(payload, &e, strictjson.Options{MaxBytes: contract.HTTPCAEnvelopeBytes, MaxDepth: 2, RejectUnknownMembers: true}) != nil {
		return ErrUnavailable
	}
	defer clear(e.Key)
	if e.Version != 1 || e.Installation != installation || string(e.Certificate) != string(r.certificate) {
		return ErrUnavailable
	}
	root, err := publicCertificate(r.certificate)
	if err != nil {
		return err
	}
	key, err := x509.ParseECPrivateKey(e.Key)
	if err != nil || key.Curve != elliptic.P256() || !key.PublicKey.Equal(root.PublicKey) {
		return ErrUnavailable
	}
	return nil
}

// VerifyBackupTx validates the selected certificate and its exact authority tuple.
func VerifyBackupTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := read(ctx, tx); err != nil {
		return err
	}
	var invalid bool
	err := tx.QueryRowContext(ctx, `WITH selected AS (
 SELECT (SELECT installation_id FROM gateway_meta WHERE singleton=1) AS owner,handle,revision FROM http_ca WHERE handle IS NOT NULL
 ), active AS (
 SELECT owner,handle,revision FROM keyring_authorities WHERE kind='http_ca'
 ) SELECT EXISTS(SELECT * FROM selected EXCEPT SELECT * FROM active) OR EXISTS(SELECT * FROM active EXCEPT SELECT * FROM selected)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return ErrUnavailable
	}
	return nil
}
