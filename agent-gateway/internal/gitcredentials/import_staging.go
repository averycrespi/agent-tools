package gitcredentials

import (
	"context"
	"database/sql"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
)

// StageConfigurationDefinitionTx creates only metadata with unset material.
func StageConfigurationDefinitionTx(ctx context.Context, tx *sql.Tx, id string, def contract.GitCredentialDefinition, deleted bool, now time.Time) error {
	canonical, err := Normalize(def)
	if err != nil || tx == nil || !contract.ValidAuditID(id) || now.IsZero() {
		return ErrInvalid
	}
	var total, active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(deleted=0),0) FROM git_credentials`).Scan(&total, &active); err != nil {
		return err
	}
	if total >= identityLimit || !deleted && active >= contract.GitCredentials {
		return ErrLimit
	}
	if _, err := resourcekeys.ReserveTx(ctx, tx, resourcekeys.GitCredential, id, canonical.Key); err != nil {
		return err
	}
	timestamp := now.UTC().Format(contract.AuditTimestampLayout)
	if _, err := tx.ExecContext(ctx, `INSERT INTO git_credentials(id,name,origin,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,1,0,?,?,?)`, id, canonical.Name, canonical.Origin, canonical.Recipe.Header, canonical.Recipe.Prefix, deleted, timestamp, timestamp); err != nil {
		return err
	}
	if err := authorization.AdvanceGitCredentialRevisionTx(ctx, tx); err != nil {
		return err
	}
	return audit.MutationTx(ctx, tx, now, "git_credential", "create", contract.AuditTarget{Type: "git_credential", ID: id})
}

// SetupFactsTx is a focused input for stopped doctor/setup consumers, not a
// material probe or an alternate authorization path.
func SetupFactsTx(ctx context.Context, tx *sql.Tx) ([]contract.CredentialSetupFact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM git_credentials WHERE deleted=0 AND material_revision=0 AND handle IS NULL ORDER BY id LIMIT ?`, contract.GitCredentials+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []contract.CredentialSetupFact{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, contract.CredentialSetupFact{ID: id, Kind: "git_credential", Action: "supply_value"})
	}
	if len(out) > contract.GitCredentials {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
