package resourcekeys

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ConfigurationLimit bounds retained reservations independently of live-resource
// capacities. Export fails closed rather than truncating this permanent inventory.
const ConfigurationLimit = contract.ConfigV2KeyBindings

type ConfigurationKey struct {
	Kind       Kind
	ResourceID string
	Key        string
}

// ConfigurationTx reads only public key bindings on the caller's snapshot.
// ResourceID is an internal join key and must never enter a configuration file.
func ConfigurationTx(ctx context.Context, tx *sql.Tx) ([]ConfigurationKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind,resource_id,key FROM resource_keys ORDER BY kind,key LIMIT ?`, ConfigurationLimit+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ConfigurationKey{}
	for rows.Next() {
		var key ConfigurationKey
		if err := rows.Scan(&key.Kind, &key.ResourceID, &key.Key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	if len(out) > ConfigurationLimit {
		return nil, errors.New("configuration reservation limit exceeded")
	}
	return out, rows.Err()
}
