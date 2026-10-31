package gitcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ConfigurationTx selects only authored definitions, not protected material or
// runtime availability. Ref is remapped before delivery by the export owner.
func ConfigurationTx(ctx context.Context, tx *sql.Tx) ([]contract.ConfigGitCredential, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,origin,header,prefix FROM git_credentials WHERE deleted=0 ORDER BY id LIMIT 257`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []contract.ConfigGitCredential{}
	for rows.Next() {
		var c contract.ConfigGitCredential
		if err := rows.Scan(&c.Ref, &c.Name, &c.Origin, &c.Recipe.Header, &c.Recipe.Prefix); err != nil {
			return nil, err
		}
		canonical, err := Normalize(c.GitCredentialDefinition)
		if err != nil || canonical != c.GitCredentialDefinition {
			return nil, ErrInvalid
		}
		out = append(out, c)
	}
	if len(out) > contract.GitCredentials {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
