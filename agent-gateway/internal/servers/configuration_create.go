package servers

import (
	"context"
	"database/sql"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ReserveConfigurationTargetTx creates only the permanent inactive namespace.
func ReserveConfigurationTargetTx(ctx context.Context, tx *sql.Tx, id, key string, now time.Time) error {
	if !validID(id) || !namespacePattern.MatchString(key) || key == contract.SyntheticServerNamespace {
		return ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_identities(id,namespace,created_at) VALUES(?,?,?)`, id, key, formatTime(now)); err != nil {
		return err
	}
	return audit.MutationTx(ctx, tx, now, "server", "create", contract.AuditTarget{Type: "server", ID: id})
}
