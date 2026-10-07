package httpcredentials

import (
	"context"
	"database/sql"
)

// VerifyBackupTx validates configuration and complete encrypted-recovery selections.
func VerifyBackupTx(ctx context.Context, tx *sql.Tx) error {
	if err := validateTx(ctx, tx); err != nil {
		return err
	}
	var invalid bool
	err := tx.QueryRowContext(ctx, `WITH selected AS (
 SELECT id AS owner,handle,material_revision AS revision FROM http_credentials WHERE handle IS NOT NULL AND deleted=0
 ), active AS (
 SELECT owner,handle,revision FROM keyring_authorities WHERE kind='http_credential'
 ) SELECT EXISTS(SELECT * FROM selected EXCEPT SELECT * FROM active) OR EXISTS(SELECT * FROM active EXCEPT SELECT * FROM selected)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return ErrInvalid
	}
	return nil
}
