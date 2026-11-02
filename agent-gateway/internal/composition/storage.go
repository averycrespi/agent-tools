package composition

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

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

// TrafficEvidence binds a stopped inspection to the exact closed generation.
// It does not certify integrity or permit ignoring live sidecars.
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
	return invocation.InitializeTraffic(ctx, owner, store, generation, invocation.DefaultTrafficConfig())
}

// openOptionalTraffic retains installation ownership while OpenTraffic validates
// safe paths and the complete WAL-aware identity/schema/evidence before attaching
// readers or admitting writes. Immutable inspection belongs only to closed artifacts.
func openOptionalTraffic(ctx context.Context, owner *gatewaypaths.Ownership, installation, generation string, config invocation.TrafficConfig) (*invocation.TrafficStore, error) {
	return invocation.OpenTraffic(ctx, owner, installation, generation, config)
}

func (built *Composition) Traffic() *invocation.TrafficStore { return built.traffic }
