package composition

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// InspectTraffic verifies only a closed selected generation. No writer, schema
// upgrade or protected provider is constructed for a maintenance plan.
func InspectTraffic(ctx context.Context, owner *gatewaypaths.Ownership, identity storage.Identity, budget int64) ([32]byte, error) {
	return InspectTrafficLayout(ctx, owner.Layout(), identity, budget)
}

// InspectTrafficLayout also permits read-only observation without stopped ownership.
func InspectTrafficLayout(ctx context.Context, layout gatewaypaths.Layout, identity storage.Identity, budget int64) ([32]byte, error) {
	var seal [32]byte
	if !ValidTrafficBudget(budget) {
		return seal, invocation.ErrInvalidInput
	}
	path := layout.Database
	if identity.TrafficGeneration == "" {
		if err := invocation.VerifyLegacyEvidence(ctx, path, identity.SchemaVersion); err != nil {
			return seal, err
		}
	} else {
		path = filepath.Join(layout.Root, "traffic-"+identity.TrafficGeneration+".db")
		if err := storage.RequireClosedGeneration(path); err != nil {
			return seal, err
		}
		if err := invocation.VerifyTrafficFile(ctx, path, identity.InstallationID, identity.TrafficGeneration, trafficConfiguration(budget)); err != nil {
			return seal, err
		}
	}
	evidence, err := InspectTrafficEvidence(layout, identity, budget)
	if err == nil && !evidence.Present {
		err = storage.ErrPlanChanged
	}
	return evidence.Seal, err
}

// TrafficEvidence binds a restore plan even when a closed generation is missing
// or corrupt. It does not certify integrity or permit ignoring live sidecars.
type TrafficEvidence struct {
	Present bool
	Seal    [32]byte
}

func InspectTrafficEvidence(layout gatewaypaths.Layout, identity storage.Identity, budget int64) (TrafficEvidence, error) {
	var evidence TrafficEvidence
	if !ValidTrafficBudget(budget) || (identity.TrafficGeneration != "" && !contract.ValidAuditID(identity.TrafficGeneration)) {
		return evidence, invocation.ErrInvalidInput
	}
	path := layout.Database
	if identity.TrafficGeneration != "" {
		path = filepath.Join(layout.Root, "traffic-"+identity.TrafficGeneration+".db")
	}
	if err := storage.RequireClosedGeneration(path); err != nil {
		return evidence, err
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return evidence, nil
		}
		return evidence, err
	}
	file, err := os.Open(path)
	if err != nil {
		return evidence, err
	}
	hash := sha256.New()
	count, readErr := io.Copy(hash, io.LimitReader(file, budget+(1<<30)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return evidence, err
	}
	if count > budget+(1<<30) {
		return evidence, storage.ErrInspectionUnavailable
	}
	if err := storage.RequireClosedGeneration(path); err != nil {
		return evidence, err
	}
	evidence.Present = true
	copy(evidence.Seal[:], hash.Sum(nil))
	return evidence, nil
}

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
func MigrateStorage(ctx context.Context, root, expectedInstallation, generation string, budget int64, approvals ...storage.StoppedApproval) (result storage.Identity, resultErr error) {
	effect := "unchanged"
	defer func() {
		if resultErr != nil && effect != "unchanged" {
			resultErr = &storage.OperationError{Effect: effect, Cause: resultErr}
		}
	}()
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
	if err := storage.ApproveStopped(ctx, owner, approvals); err != nil {
		return storage.Identity{}, err
	}
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
	effect = "staged"
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
	effect = "uncertain"
	if err = storage.InstallReplacement(owner, stage); err != nil {
		return storage.Identity{}, err
	}
	return identity, nil
}

func VerifyStorage(ctx context.Context, root string) (storage.Identity, error) {
	return VerifyStorageBudget(ctx, root, DefaultTrafficBudget)
}

func VerifyStorageBudget(ctx context.Context, root string, budget int64, approvals ...storage.StoppedApproval) (storage.Identity, error) {
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
	}, approvals...)
}

func (built *Composition) Traffic() *invocation.TrafficStore { return built.traffic }
