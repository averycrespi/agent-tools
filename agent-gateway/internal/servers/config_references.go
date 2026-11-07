package servers

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ConfigurationReferencesTx reads only target keys for a single-grant export;
// it does not traverse transports, credentials, catalogs or runtime state.
func ConfigurationReferencesTx(ctx context.Context, tx *sql.Tx) (map[string]contract.ConfigServer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,namespace FROM server_identities ORDER BY namespace LIMIT 1025`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]contract.ConfigServer{}
	for rows.Next() {
		var id string
		var value contract.ConfigServer
		if err := rows.Scan(&id, &value.Namespace); err != nil {
			return nil, err
		}
		out[id] = value
	}
	if len(out) > 1024 {
		return nil, ErrResourceLimit
	}
	return out, rows.Err()
}
