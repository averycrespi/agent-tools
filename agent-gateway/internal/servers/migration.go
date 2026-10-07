package servers

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
)

// MigrationDependency is a stopped snapshot, including disabled but nondeleted
// servers. NULL required selections must not disappear from completeness checks.
type MigrationDependency struct {
	ID           string
	Transport    []byte
	Authority    AuthorityMetadata
	Registration OAuthRegistrationAuthority
}

func MigrationDependenciesTx(ctx context.Context, tx *sql.Tx, inspect func(MigrationDependency) error) error {
	if err := VerifyBackupCredentialsTx(ctx, tx); err != nil {
		return err
	}
	after := ""
	for {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM servers WHERE desired_state<>'deleted' AND id>? ORDER BY id LIMIT 1`, after).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		dependency, err := migrationDependencyTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = inspect(dependency); err != nil {
			return err
		}
		after = id
	}
}

func MigrationMaterialTx(ctx context.Context, tx *sql.Tx, kind keyring.RecordKind, handle keyring.Handle, inspect func(MigrationDependency) error) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT c.server_id FROM server_credentials c JOIN servers s ON s.id=c.server_id WHERE c.kind=? AND c.handle=? AND s.desired_state<>'deleted'`, kind, string(handle)).Scan(&id); err != nil {
		return err
	}
	dependency, err := migrationDependencyTx(ctx, tx, id)
	if err != nil {
		return err
	}
	return inspect(dependency)
}

func migrationDependencyTx(ctx context.Context, tx *sql.Tx, id string) (result MigrationDependency, err error) {
	result.ID = id
	if err = tx.QueryRowContext(ctx, `SELECT transport_json FROM servers WHERE id=?`, id).Scan(&result.Transport); err != nil {
		return result, err
	}
	result.Authority, err = authorityTx(ctx, tx, id)
	if err != nil {
		return result, err
	}
	transport, err := DecodeTransport(result.Transport)
	if err != nil {
		return result, err
	}
	if http, ok := transport.(contract.StreamableHTTPTransport); ok {
		if _, ok := http.Authentication.(contract.OAuthAuthentication); ok {
			result.Registration, err = oauthRegistrationTx(ctx, tx, id)
		}
	}
	return result, err
}
