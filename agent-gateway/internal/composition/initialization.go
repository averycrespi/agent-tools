package composition

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type InitializationResult struct {
	storage.Identity
	Certificate []byte
}

// InitializeOwned completes only this recorded fresh installation. The caller
// retains one stopped owner from consent revalidation through final publication.
// Uncertain generic storage/custody effects are never recovered by this path.
func InitializeOwned(ctx context.Context, owner *gatewaypaths.Ownership, plan gatewaypaths.InitializationPlan, clock Clock, entropy io.Reader) (InitializationResult, error) {
	return initializeOwned(ctx, owner, plan, clock, entropy, nil)
}

func initializeOwned(ctx context.Context, owner *gatewaypaths.Ownership, plan gatewaypaths.InitializationPlan, clock Clock, entropy io.Reader, fault func(string) error) (identity InitializationResult, err error) {
	if owner == nil || clock == nil || entropy == nil {
		return identity, errors.New("initialization dependencies are incomplete")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	layout := owner.Layout()
	var imported configimport.Input
	if plan.ImportDigest != "" {
		data, readErr := gatewaypaths.ReadInitializationInput(layout, plan)
		if readErr != nil {
			return identity, readErr
		}
		imported, err = configimport.Decode(data)
		if err != nil || imported.Digest != plan.ImportDigest {
			return identity, errors.Join(err, storage.ErrPlanChanged)
		}
	}
	recorded, complete, err := gatewaypaths.ReadInitialization(layout)
	if err != nil || recorded != plan || complete {
		return identity, errors.Join(err, storage.ErrPlanChanged)
	}
	step := func(name, evidence string) error {
		if fault != nil {
			if err := fault(name + "-effect"); err != nil {
				return err
			}
		}
		if err := gatewaypaths.SealInitialization(owner, name, evidence); err != nil {
			return err
		}
		if fault != nil {
			return fault(name + "-sealed")
		}
		return nil
	}
	// Unknown artifacts never become permission to continue setup.
	directory, err := os.Open(layout.Root)
	if err != nil {
		return identity, err
	}
	entries, readErr := directory.ReadDir(16)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return identity, err
	}
	if len(entries) >= 16 {
		return identity, storage.ErrPlanChanged
	}
	allowed := map[string]bool{gatewaypaths.LockName: true, gatewaypaths.InitializationName: true, gatewaypaths.DatabaseName: true, gatewaypaths.MasterKeyName: true, gatewaypaths.PublicCertificateName: true}
	for _, base := range []string{gatewaypaths.DatabaseName, "traffic-" + plan.Traffic + ".db"} {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			allowed[base+suffix] = true
		}
	}
	bearerDirectory, directoryErr := filepath.EvalSymlinks(filepath.Dir(plan.Bearer))
	if directoryErr != nil {
		return identity, directoryErr
	}
	if bearerDirectory == layout.Root {
		allowed[filepath.Base(plan.Bearer)] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return identity, storage.ErrPlanChanged
		}
	}
	// Inspect all existing authority and recorded files before opening a writer.
	var inspection initializationInspection
	_, statErr := os.Lstat(layout.Database)
	switch {
	case statErr == nil:
		inspection, err = inspectInitialization(ctx, owner, plan, clock)
		if err != nil {
			return identity, err
		}
	case errors.Is(statErr, os.ErrNotExist):
		for _, entry := range entries {
			if entry.Name() != gatewaypaths.LockName && entry.Name() != gatewaypaths.InitializationName {
				return identity, storage.ErrPlanChanged
			}
		}
		receipt, readErr := gatewaypaths.InitializationReceipt(layout, "control")
		if readErr != nil || receipt != "" {
			return identity, errors.Join(readErr, storage.ErrPlanChanged)
		}
	default:
		return identity, statErr
	}
	if err := gatewaypaths.CheckCertificateDestination(filepath.Join(layout.Root, gatewaypaths.PublicCertificateName), inspection.ca.Certificate); err != nil {
		return identity, err
	}
	if !inspection.admin {
		if _, err := os.Lstat(plan.Bearer); !errors.Is(err, os.ErrNotExist) {
			return identity, errors.Join(err, admin.ErrSecretPublication)
		}
		if err := gatewaypaths.ValidateOutputDirectory(filepath.Dir(plan.Bearer)); err != nil {
			return identity, err
		}
	}
	if err := gatewaypaths.ConfirmInitialization(owner, plan); err != nil {
		return identity, err
	}
	var store *storage.Store
	if errors.Is(statErr, os.ErrNotExist) {
		store, err = storage.Initialize(ctx, owner, plan.Installation)
	} else {
		store, err = storage.Open(ctx, owner)
	}
	if err != nil {
		return identity, err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, store.Close())
		}
	}()
	if err := step("control", plan.Installation); err != nil {
		return identity, err
	}
	// SetupCustody itself reconciles a complete unbound key and refuses a missing
	// established key, partial publication, or any changed database/key binding.
	if err := gatewaypaths.SealInitialization(owner, "custody-attempt", "attempt"); err != nil {
		return identity, err
	}
	if fault != nil {
		if err := fault("custody-attempt"); err != nil {
			return identity, err
		}
	}
	if err := SetupSecrets(ctx, owner, store, clock); err != nil {
		return identity, err
	}
	key, err := gatewaypaths.MasterKey(owner, nil)
	if err != nil {
		return identity, err
	}
	keySeal := initializationDigest(key)
	clear(key)
	if err := step("custody", keySeal); err != nil {
		return identity, err
	}
	selected, err := store.SelectedTraffic(ctx)
	if err != nil {
		return identity, err
	}
	if selected == "" {
		if err := InitializeTraffic(ctx, owner, store, plan.Traffic); err != nil {
			return identity, err
		}
	} else if selected != plan.Traffic {
		return identity, storage.ErrPlanChanged
	}
	if err := step("traffic", plan.Traffic); err != nil {
		return identity, err
	}
	if !inspection.admin {
		attempted := false
		if err := store.View(ctx, func(tx *sql.Tx) error {
			var err error
			attempted, err = admin.InitializationAttemptedTx(ctx, tx)
			return err
		}); err != nil {
			return identity, err
		}
		if attempted {
			return identity, admin.ErrSecretPublication
		}
		if _, err := admin.NewService(store, clock, entropy).Initialize(ctx, initializationSink{plan.Bearer}); err != nil {
			return identity, err
		}
	}
	bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{FilePath: plan.Bearer})
	if err != nil {
		return identity, err
	}
	if err := store.View(ctx, func(tx *sql.Tx) error { _, err := admin.AuthenticateTx(ctx, tx, bearer, clock.Now()); return err }); err != nil {
		return identity, err
	}
	if err := step("administrator", initializationDigest([]byte(bearer))); err != nil {
		return identity, err
	}
	// Close/checkpoint before immutable CA inspection; ownership remains held.
	if err := store.Checkpoint(ctx); err != nil {
		return identity, err
	}
	if err := store.Close(); err != nil {
		return identity, err
	}
	closed = true
	operation := "create"
	if inspection.ca.Revision != "" && inspection.ca.Revision != "0" {
		operation = "export"
	}
	certificate, err := httpCAOwned(ctx, owner, plan.Installation, operation, clock, entropy, productionProvider, CACallbacks{})
	if err != nil {
		return identity, err
	}
	if err := validateInitializationSigner(ctx, owner, plan.Installation, clock, entropy); err != nil {
		return identity, err
	}
	if err := step("ca", initializationDigest(certificate)); err != nil {
		return identity, err
	}
	certificatePath := filepath.Join(layout.Root, gatewaypaths.PublicCertificateName)
	if err := gatewaypaths.PublishCertificate(certificatePath, certificate, nil); err != nil {
		return identity, err
	}
	if err := step("certificate", initializationDigest(certificate)); err != nil {
		return identity, err
	}
	if plan.ImportDigest != "" {
		if err := initializeConfiguration(ctx, owner, imported, clock, entropy, fault); err != nil {
			return identity, err
		}
		if err := step("import", plan.ImportDigest); err != nil {
			return identity, err
		}
	}
	verified, err := inspectInitialization(ctx, owner, plan, clock)
	if err != nil {
		return identity, err
	}
	if !verified.admin || !verified.ca.Selected || verified.snapshot.Identity.TrafficGeneration != plan.Traffic {
		return identity, storage.ErrPlanChanged
	}
	if err := step("complete", "complete"); err != nil {
		return identity, err
	}
	if err := gatewaypaths.RemoveInitializationInput(owner); err != nil {
		return identity, err
	}
	identity.Identity = verified.snapshot.Identity
	identity.Certificate = append([]byte(nil), certificate...)
	return identity, nil
}

type initializationInspection struct {
	snapshot storage.Inspection
	admin    bool
	ca       httpca.Inspection
}

func inspectInitialization(ctx context.Context, owner *gatewaypaths.Ownership, plan gatewaypaths.InitializationPlan, clock Clock) (result initializationInspection, err error) {
	layout := owner.Layout()
	result.snapshot, err = storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		var err error
		result.admin, err = admin.InitializedTx(ctx, tx)
		if err != nil {
			return err
		}
		if !result.admin {
			attempted, err := admin.InitializationAttemptedTx(ctx, tx)
			if err != nil {
				return err
			}
			if attempted {
				return admin.ErrSecretPublication
			}
		}
		result.ca, err = httpca.InspectTx(ctx, tx)
		if err != nil {
			return err
		}
		digest, err := configimport.ReceiptTx(ctx, tx)
		if err != nil {
			return err
		}
		if digest != "" {
			if digest != plan.ImportDigest {
				return storage.ErrPlanChanged
			}
			if err := configimport.VerifyReceiptTx(ctx, tx, plan.ImportDigest); err != nil {
				return err
			}
		}
		receipt, err := gatewaypaths.InitializationReceipt(layout, "import")
		if err != nil || receipt != "" && receipt != digest {
			return errors.Join(err, storage.ErrPlanChanged)
		}
		return keyring.InspectInitialCustodyTx(ctx, owner, tx)
	})
	if err != nil {
		return result, err
	}
	if result.snapshot.Marked || result.snapshot.Identity.SchemaVersion != storage.CurrentSchema || result.snapshot.Identity.InstallationID != plan.Installation {
		return result, storage.ErrPlanChanged
	}
	if result.ca.Unsettled || (result.ca.Revision != "0" && !result.ca.Selected) {
		return result, httpca.ErrUnavailable
	}
	expected := map[string]string{"control": plan.Installation}
	key, keyErr := gatewaypaths.MasterKey(owner, nil)
	switch {
	case keyErr == nil:
		expected["custody"] = initializationDigest(key)
		clear(key)
	case !errors.Is(keyErr, os.ErrNotExist):
		return result, keyErr
	default:
		attempt, err := gatewaypaths.InitializationReceipt(layout, "custody-attempt")
		if err != nil {
			return result, err
		}
		if attempt != "" {
			return result, keyring.ErrCustodyUnavailable
		}
	}
	if result.snapshot.Identity.TrafficGeneration != "" {
		if result.snapshot.Identity.TrafficGeneration != plan.Traffic {
			return result, storage.ErrPlanChanged
		}
		if _, err := InspectTraffic(ctx, owner, result.snapshot.Identity, DefaultTrafficBudget); err != nil {
			return result, err
		}
		expected["traffic"] = plan.Traffic
	}
	if result.admin {
		bearer, err := controlclient.AcquireAdminBearer(controlclient.BearerOptions{FilePath: plan.Bearer})
		if err != nil {
			return result, err
		}
		if _, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error { _, err := admin.AuthenticateTx(ctx, tx, bearer, clock.Now()); return err }); err != nil {
			return result, err
		}
		expected["administrator"] = initializationDigest([]byte(bearer))
	}
	if result.ca.Selected {
		expected["ca"] = initializationDigest(result.ca.Certificate)
	}
	certificatePath := filepath.Join(layout.Root, gatewaypaths.PublicCertificateName)
	if _, err := os.Lstat(certificatePath); err == nil {
		if err := gatewaypaths.CheckCertificateDestination(certificatePath, result.ca.Certificate); err != nil {
			return result, err
		}
		expected["certificate"] = initializationDigest(result.ca.Certificate)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, name := range []string{"control", "custody", "traffic", "administrator", "ca", "certificate"} {
		receipt, err := gatewaypaths.InitializationReceipt(layout, name)
		if err != nil {
			return result, err
		}
		if receipt != "" && receipt != expected[name] {
			return result, storage.ErrPlanChanged
		}
	}
	return result, nil
}

func validateInitializationSigner(ctx context.Context, owner *gatewaypaths.Ownership, installation string, clock Clock, entropy io.Reader) (err error) {
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	provider, err := productionProvider(installation)
	if err != nil {
		return err
	}
	if err := provider.UseDatabaseCustody(ctx, owner, store); err != nil {
		return err
	}
	coordinator := keyring.NewCoordinator(provider, store, clock, entropy)
	defer coordinator.Drain()
	ca, err := httpca.New(store, coordinator, installation, clock, entropy)
	if err != nil {
		return err
	}
	defer ca.Close()
	_, err = ca.Load(ctx)
	return err
}

func initializationDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// Initialization retains uncertain/partial files instead of the general sink's
// failure cleanup. A future resume never overwrites a publication attempt.
type initializationSink struct{ path string }

func (sink initializationSink) Publish(value string) error {
	file, err := gatewaypaths.CreateOwnerOnlyFile(sink.path)
	if err != nil {
		return err
	}
	contents := value + "\n"
	count, writeErr := io.WriteString(file, contents)
	if count != len(contents) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(sink.path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
