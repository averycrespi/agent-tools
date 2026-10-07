package gitcredentials

import (
	"context"
	"database/sql"
)

// VerifyEncryptedBackupTx validates complete material selections in addition to
// the ordinary configuration checks, which permit explicitly unavailable material.
func VerifyEncryptedBackupTx(ctx context.Context, tx *sql.Tx) error {
	if err := validateTx(ctx, tx); err != nil {
		return err
	}
	var invalid bool
	err := tx.QueryRowContext(ctx, `WITH selected AS (
 SELECT id AS owner,handle,material_revision AS revision FROM git_credentials WHERE handle IS NOT NULL AND deleted=0
 ), active AS (
 SELECT owner,handle,revision FROM keyring_authorities WHERE kind='git_credential'
 ) SELECT EXISTS(SELECT * FROM selected EXCEPT SELECT * FROM active) OR EXISTS(SELECT * FROM active EXCEPT SELECT * FROM selected)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return ErrInvalid
	}
	return nil
}
