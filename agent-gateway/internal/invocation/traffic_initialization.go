package invocation

import (
	"context"
	"database/sql"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// InitializeTraffic publishes an empty generation during initialization, before
// authority is exposed. Failure retains unselected output; it never copies or
// deletes historical embedded traffic.
func InitializeTraffic(ctx context.Context, owner *gatewaypaths.Ownership, control *storage.Store, generation string, config TrafficConfig) error {
	if owner == nil || control == nil || !config.valid() || !validOpaqueInvocationID(generation) {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if control.Latched() {
		return storage.ErrStorageLatched
	}
	selected, err := control.SelectedTraffic(ctx)
	if err != nil {
		return err
	}
	if selected != "" {
		return ErrInvalidState
	}
	// Initialization cannot turn a historical single-store installation into a
	// new empty history selection or discard its embedded evidence.
	if err := control.View(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrInvalidState
		}
		return nil
	}); err != nil {
		return err
	}
	identity, err := control.Identity(ctx)
	if err != nil {
		return err
	}
	traffic, err := openTraffic(ctx, owner, identity.InstallationID, generation, config, true, nil)
	if err != nil {
		return err
	}
	if err = traffic.Close(); err != nil {
		return err
	}
	return control.SelectTraffic(ctx, "", generation)
}
