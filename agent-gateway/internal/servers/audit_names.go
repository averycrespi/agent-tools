package servers

import (
	"context"
	"database/sql"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// AuditTargetNamesTx reads live names in the audit reader's snapshot.
func (repository *Repository) AuditTargetNamesTx(ctx context.Context, tx *sql.Tx, targets []contract.AuditTarget) (map[contract.AuditTarget]string, error) {
	if tx == nil || len(targets) > contract.AuditPageLimit {
		return nil, ErrStorageUnavailable
	}
	names := make(map[contract.AuditTarget]string)
	ids := []any{}
	for _, target := range targets {
		if target.Type == "server" {
			if target.ID == contract.SyntheticServerID {
				names[target] = "Gateway self-service tools"
			} else {
				ids = append(ids, target.ID)
			}
		}
	}
	if len(ids) == 0 {
		return names, nil
	}
	//nolint:gosec // Only bounded placeholder punctuation is generated; all IDs are bound values.
	rows, err := tx.QueryContext(ctx, "SELECT id, display_name FROM servers WHERE desired_state != 'deleted' AND id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[contract.AuditTarget{Type: "server", ID: id}] = name
	}
	return names, rows.Err()
}
