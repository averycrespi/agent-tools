package composition

import (
	"context"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

const DefaultTrafficBudget int64 = 4 << 30

func trafficConfiguration(budget int64) invocation.TrafficConfig {
	config := invocation.DefaultTrafficConfig()
	limit, _ := contract.FixedLimitByName("invocation_audit_rows")
	config.RetainedRecords = limit.Maximum
	if budget != 0 {
		config.BudgetBytes = budget
	}
	return config
}

func ValidTrafficBudget(budget int64) bool { return budget >= 1<<20 && budget <= 16<<30 }

func InitializeTraffic(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, generation string) error {
	selected, err := store.SelectedTraffic(ctx)
	if err != nil {
		return err
	}
	if selected != "" {
		identity, err := store.Identity(ctx)
		if err != nil {
			return err
		}
		traffic, err := invocation.OpenTraffic(ctx, owner, identity.InstallationID, selected, trafficConfiguration(DefaultTrafficBudget))
		if err != nil {
			return err
		}
		return traffic.Close()
	}
	releaseSpace, err := gatewaypaths.ReserveHeadroom(owner.Layout().Root, DefaultTrafficBudget+(1<<30))
	if err != nil {
		return err
	}
	defer releaseSpace()
	return invocation.MigrateTraffic(ctx, owner, store, generation, invocation.DefaultTrafficConfig())
}

// MigrateStorage is explicit stopped staging, never serve-time migration. The
// original control generation remains authoritative until the final rename.
func MigrateStorage(ctx context.Context, root, expectedInstallation, generation string, budget int64) (storage.Identity, error) {
	if !ValidTrafficBudget(budget) {
		return storage.Identity{}, invocation.ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return storage.Identity{}, err
	}
	defer func() { _ = owner.Close() }()
	releaseSpace, err := gatewaypaths.ReserveHeadroom(owner.Layout().Root, budget+2*(1<<30))
	if err != nil {
		return storage.Identity{}, err
	}
	defer releaseSpace()
	control, err := storage.OpenMigrationSource(ctx, owner)
	if err != nil {
		return storage.Identity{}, err
	}
	if control.Latched() {
		_ = control.Close()
		return storage.Identity{}, storage.ErrStorageLatched
	}
	sourceIdentity, err := control.Identity(ctx)
	if err != nil {
		_ = control.Close()
		return storage.Identity{}, err
	}
	if sourceIdentity.InstallationID != expectedInstallation {
		_ = control.Close()
		return storage.Identity{}, invocation.ErrInvalidInput
	}
	// Released schema 17 has failure diagnostics, but no traffic selector.
	if sourceIdentity.SchemaVersion >= 18 {
		selected, err := control.SelectedTraffic(ctx)
		if err != nil || selected != "" {
			_ = control.Close()
			return storage.Identity{}, errors.Join(err, invocation.ErrInvalidState)
		}
	}
	stage := owner.Layout().Database + ".traffic-migration"
	err = control.BackupTo(ctx, stage)
	err = errors.Join(err, control.Close())
	if err != nil {
		return storage.Identity{}, err
	}
	replacement, err := storage.OpenReplacement(ctx, owner, stage)
	if err != nil {
		return storage.Identity{}, err
	}
	err = invocation.MigrateTraffic(ctx, owner, replacement, generation, trafficConfiguration(budget))
	if err == nil {
		err = replacement.Checkpoint(ctx)
	}
	identity, identityErr := replacement.Identity(ctx)
	err = errors.Join(err, identityErr, replacement.Close())
	if err != nil {
		return storage.Identity{}, err
	}
	if err = storage.InstallReplacement(owner, stage); err != nil {
		return storage.Identity{}, err
	}
	return identity, nil
}

func VerifyStorage(ctx context.Context, root string) (storage.Identity, error) {
	return VerifyStorageBudget(ctx, root, DefaultTrafficBudget)
}

func VerifyStorageBudget(ctx context.Context, root string, budget int64) (storage.Identity, error) {
	if !ValidTrafficBudget(budget) {
		return storage.Identity{}, invocation.ErrInvalidInput
	}
	return storage.VerifyCurrentWithTraffic(ctx, root, func(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store) error {
		generation, err := store.SelectedTraffic(ctx)
		if err != nil {
			return err
		}
		if generation == "" {
			return storage.ErrTrafficUnselected
		}
		identity, err := store.Identity(ctx)
		if err != nil {
			return err
		}
		traffic, err := invocation.OpenTraffic(ctx, owner, identity.InstallationID, generation, trafficConfiguration(budget))
		if err != nil {
			return err
		}
		return traffic.Close()
	})
}

func (built *Composition) Traffic() *invocation.TrafficStore { return built.traffic }
