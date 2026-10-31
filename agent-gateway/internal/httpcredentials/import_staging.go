package httpcredentials

import (
	"context"
	"database/sql"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// StageImportedDefinitionTx reserves an unset definition only in the caller's
// stopped import-stage transaction. The importer owns fresh identity mapping,
// transaction rollback and stage validation before publication. There is no live
// service/API counterpart. No provider, generation, fence or dummy handle is made.
func StageImportedDefinitionTx(ctx context.Context, tx *sql.Tx, id string, def Definition, now time.Time) error {
	canonical, err := Normalize(def)
	if err != nil || tx == nil || !contract.ValidAuditID(id) || now.IsZero() {
		return ErrInvalid
	}
	var total, active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(deleted=0),0) FROM http_credentials`).Scan(&total, &active); err != nil {
		return err
	}
	if total >= identityLimit || active >= contract.HTTPPolicyCredentials {
		return ErrLimit
	}
	timestamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,0,0,?,?)`, id, canonical.Name, canonical.Boundary.Host, canonical.Boundary.Port, canonical.Boundary.AllowWildcard, canonical.Recipe.Header, canonical.Recipe.Prefix, timestamp, timestamp); err != nil {
		return err
	}
	return audit.MutationTx(ctx, tx, now, "http_credential", "create", contract.AuditTarget{Type: "http_credential", ID: id})
}

// SetupFactsTx supplies only missing setup, on the stopped doctor's read view.
// Failed/invalidated material is deliberately not reported as never configured.
func SetupFactsTx(ctx context.Context, tx *sql.Tx) ([]contract.CredentialSetupFact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM http_credentials WHERE deleted=0 AND material_revision=0 AND handle IS NULL ORDER BY id LIMIT ?`, contract.HTTPPolicyCredentials+1)
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
		out = append(out, contract.CredentialSetupFact{ID: id, Kind: "http_credential", Action: "supply_value"})
	}
	if len(out) > contract.HTTPPolicyCredentials {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
