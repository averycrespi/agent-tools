package gitcredentials

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// AuditTargetNamesTx reads only nonsecret recognition, never credential material.
func (service *Service) AuditTargetNamesTx(ctx context.Context, tx *sql.Tx, targets []contract.AuditTarget) (map[contract.AuditTarget]string, error) {
	if tx == nil || len(targets) > contract.AuditRetention {
		return nil, ErrInvalid
	}
	names := make(map[contract.AuditTarget]string)
	ids := []any{}
	for _, target := range targets {
		if target.Type == "git_credential" {
			ids = append(ids, target.ID)
		}
	}
	if len(ids) == 0 {
		return names, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM git_credentials WHERE deleted = 0 AND id IN (SELECT value FROM json_each(?))", string(encoded))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[contract.AuditTarget{Type: "git_credential", ID: id}] = name
	}
	return names, rows.Err()
}
