package resourcekeys

import (
	"context"
	"database/sql"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ReserveConfigurationTx retains an explicit key without recreating its resource.
func ReserveConfigurationTx(ctx context.Context, tx *sql.Tx, kind Kind, id, key string, now time.Time) error {
	if _, err := ReserveTx(ctx, tx, kind, id, key); err != nil {
		return err
	}
	entity := string(kind)
	if kind == Agent {
		entity = "principal"
	}
	if kind == MCPGrant {
		entity = "grant"
	}
	return audit.MutationTx(ctx, tx, now, entity, "create", contract.AuditTarget{Type: entity, ID: id})
}
