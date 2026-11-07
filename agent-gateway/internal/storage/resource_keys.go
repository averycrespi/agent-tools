package storage

import (
	"context"
	"fmt"
)

// Reservations may outlive their resource, but every retained resource must
// have exactly one reservation. Startup never repairs a missing mapping.
func (store *Store) verifyResourceKeys(ctx context.Context) error {
	var missing int
	err := store.database.QueryRowContext(ctx, `
		SELECT count(*) FROM (
			SELECT 'agent' kind, id FROM principals
			UNION ALL SELECT 'mcp_grant', id FROM grants
			UNION ALL SELECT 'http_grant', id FROM http_grants
			UNION ALL SELECT 'git_grant', id FROM git_grants
			UNION ALL SELECT 'http_credential', id FROM http_credentials
			UNION ALL SELECT 'git_credential', id FROM git_credentials
			UNION ALL SELECT 'git_repository', id FROM git_repositories
		) r WHERE NOT EXISTS (SELECT 1 FROM resource_keys k WHERE k.kind=r.kind AND k.resource_id=r.id)
	`).Scan(&missing)
	if err != nil {
		return fmt.Errorf("validate resource key coverage: %w", err)
	}
	if missing != 0 {
		return fmt.Errorf("retained resources lack key reservations")
	}
	return nil
}
