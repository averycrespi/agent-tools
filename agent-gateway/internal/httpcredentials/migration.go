package httpcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func VerifyMigrationDependenciesTx(ctx context.Context, tx *sql.Tx) error {
	if err := VerifyBackupTx(ctx, tx); err != nil {
		return err
	}
	var missing bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM http_credentials WHERE deleted=0 AND handle IS NULL)`).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return keyring.ErrMigrationIncomplete
	}
	return nil
}

func VerifyMigrationMaterialTx(ctx context.Context, tx *sql.Tx, handle keyring.Handle, payload []byte) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM http_credentials WHERE handle=? AND deleted=0`, string(handle)).Scan(&id); err != nil {
		return err
	}
	record, err := readTx(ctx, tx, id)
	if err != nil {
		return err
	}
	var decoded generation
	if strictjson.Decode(payload, &decoded, strictjson.Options{MaxBytes: contract.HTTPCredentialValueBytes*6 + 64, MaxDepth: 2, RejectUnknownMembers: true}) != nil || decoded.Version != 1 || !contract.ValidHTTPCredentialSecret(record.Recipe, []byte(decoded.Secret)) {
		return ErrUnavailable
	}
	return nil
}
