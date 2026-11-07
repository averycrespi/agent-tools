package gitcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
)

// RetainedConfigurationTx includes deleted definitions but never material facts.
// Map keys are private joins, not output references.
func RetainedConfigurationTx(ctx context.Context, tx *sql.Tx) (map[string]contract.ConfigGitCredentialSpec, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,origin,header,prefix,deleted FROM git_credentials ORDER BY id LIMIT ?`, resourcekeys.ConfigurationLimit+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]contract.ConfigGitCredentialSpec{}
	for rows.Next() {
		var id string
		var s contract.ConfigGitCredentialSpec
		if err := rows.Scan(&id, &s.Name, &s.Origin, &s.Recipe.Header, &s.Recipe.Prefix, &s.Deleted); err != nil {
			return nil, err
		}
		if _, err := Normalize(s.GitCredentialDefinition); err != nil {
			return nil, err
		}
		out[id] = s
	}
	if len(out) > resourcekeys.ConfigurationLimit {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
