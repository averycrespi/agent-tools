package authorization

import (
	"context"
	"database/sql"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// AuditTargetNamesTx projects current recognition without hydrating policy or credentials.
func (repository *Repository) AuditTargetNamesTx(ctx context.Context, tx *sql.Tx, targets []contract.AuditTarget) (map[contract.AuditTarget]string, error) {
	if tx == nil || len(targets) > contract.AuditPageLimit {
		return nil, ErrInvalidInput
	}
	names := make(map[contract.AuditTarget]string)
	for kind, statement := range map[string]string{
		"principal":      "SELECT id, display_name FROM principals WHERE id IN (",
		"http_default":   "SELECT id, display_name FROM principals WHERE id IN (",
		"grant":          "SELECT id, coalesce(description, '') FROM grants WHERE id IN (",
		"http_grant":     "SELECT id, coalesce(description, '') FROM http_grants WHERE id IN (",
		"git_grant":      "SELECT id, coalesce(description, '') FROM git_grants WHERE id IN (",
		"git_repository": "SELECT id, name FROM git_repositories WHERE deleted = 0 AND id IN (",
	} {
		ids := []any{}
		for _, target := range targets {
			if target.Type == kind {
				ids = append(ids, target.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, statement+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				_ = rows.Close()
				return nil, err
			}
			names[contract.AuditTarget{Type: kind, ID: id}] = name
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return names, nil
}
