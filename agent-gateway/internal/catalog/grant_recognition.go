package catalog

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

// GrantToolIDsTx projects descriptor-backed identities, including readable retired
// descriptors, in the caller's snapshot. It conveys recognition, not callability.
func (repository *Repository) GrantToolIDsTx(ctx context.Context, tx *sql.Tx) (map[[2]string]string, error) {
	if tx == nil || repository.store.Latched() {
		return nil, servers.ErrStorageUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT identity.server_id, identity.upstream_name, identity.id
 FROM durable_tool_identities identity JOIN tool_descriptors descriptor ON descriptor.tool_id = identity.id
 ORDER BY identity.insertion_sequence LIMIT ?`, fixedLimit("durable_tool_identities")+1)
	if err != nil {
		return nil, mapStoreError(err)
	}
	defer func() { _ = rows.Close() }()
	ids := make(map[[2]string]string)
	count := int64(0)
	for rows.Next() {
		var server, name, id string
		if err := rows.Scan(&server, &name, &id); err != nil {
			return nil, mapStoreError(err)
		}
		count++
		if count > fixedLimit("durable_tool_identities") {
			return nil, servers.ErrResourceLimit
		}
		key := [2]string{server, name}
		if _, exists := ids[key]; exists {
			ids[key] = ""
		} else {
			ids[key] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapStoreError(err)
	}
	if err := rows.Close(); err != nil {
		return nil, mapStoreError(err)
	}
	return ids, nil
}
