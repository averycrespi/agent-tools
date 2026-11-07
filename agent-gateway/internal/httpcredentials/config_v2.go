package httpcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
)

// RetainedConfigurationTx includes deleted definitions but never material facts.
// Map keys are private joins, not output references.
func RetainedConfigurationTx(ctx context.Context, tx *sql.Tx) (map[string]contract.ConfigHTTPCredentialSpec, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,host,port,allow_wildcard,header,prefix,deleted FROM http_credentials ORDER BY id LIMIT ?`, resourcekeys.ConfigurationLimit+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]contract.ConfigHTTPCredentialSpec{}
	for rows.Next() {
		var id string
		var s contract.ConfigHTTPCredentialSpec
		if err := rows.Scan(&id, &s.Name, &s.Boundary.Host, &s.Boundary.Port, &s.Boundary.AllowWildcard, &s.Recipe.Header, &s.Recipe.Prefix, &s.Deleted); err != nil {
			return nil, err
		}
		if _, err := Normalize(Definition{Name: s.Name, Boundary: Boundary{Host: s.Boundary.Host, Port: s.Boundary.Port, AllowWildcard: s.Boundary.AllowWildcard}, Recipe: s.Recipe}); err != nil {
			return nil, err
		}
		out[id] = s
	}
	if len(out) > resourcekeys.ConfigurationLimit {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
