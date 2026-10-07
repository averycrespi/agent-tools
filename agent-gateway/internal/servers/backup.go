package servers

import (
	"context"
	"database/sql"
)

// VerifyBackupCredentialsTx checks that restored MCP selections and keyring
// authority agree in both directions. Ciphertext validation is keyring-owned.
func VerifyBackupCredentialsTx(ctx context.Context, tx *sql.Tx) error {
	var invalid bool
	err := tx.QueryRowContext(ctx, `WITH selected AS (
 SELECT server_id AS owner,kind,handle,revision FROM server_credentials WHERE handle IS NOT NULL
 ), active AS (
 SELECT owner,kind,handle,revision FROM keyring_authorities WHERE kind IN ('static_credential','oauth_client','oauth_tokens')
 ) SELECT EXISTS(SELECT * FROM selected EXCEPT SELECT * FROM active) OR EXISTS(SELECT * FROM active EXCEPT SELECT * FROM selected)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return ErrInvalidInput
	}
	return nil
}
