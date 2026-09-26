package composition

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrCAIdentity = errors.New("installation identity assertion does not match")
var ErrCAPublication = errors.New("public certificate publication failed")

type CAPlan struct {
	Root           string `json:"data_dir"`
	InstallationID string `json:"installation_id"`
	Revision       string `json:"revision"`
	HadCA          bool   `json:"existing_ca"`
}

type CACallbacks struct {
	Validate func(previous []byte) error
	Confirm  func(CAPlan) error
	Publish  func(certificate, previous []byte) error
}

// HTTPCAConfirmed keeps target inspection, confirmation, cutover and public
// publication under one stopped owner. Callbacks cannot change CA authority.
func HTTPCAConfirmed(ctx context.Context, root, installation, operation string, clock Clock, entropy io.Reader, callbacks CACallbacks) ([]byte, error) {
	return httpCA(ctx, root, installation, operation, clock, entropy, productionProvider, callbacks)
}

// CAEffectError retains the known authority effect without exposing provider details.
type CAEffectError struct {
	Effect string
	Cause  error
}

func (e *CAEffectError) Error() string { return "CA operation failed: " + e.Effect }
func (e *CAEffectError) Unwrap() error { return e.Cause }

// HTTPCA operates only on a locked, existing stopped installation. It never
// constructs the serving graph, opens listeners, or changes client trust.
func HTTPCA(ctx context.Context, root, installation, operation string, clock Clock, entropy io.Reader) ([]byte, error) {
	return httpCA(ctx, root, installation, operation, clock, entropy, productionProvider)
}

func httpCA(ctx context.Context, root, installation, operation string, clock Clock, entropy io.Reader, providerFactory func(string) (*keyring.Provider, error), callbackOptions ...CACallbacks) (certificate []byte, err error) {
	var callbacks CACallbacks
	if len(callbackOptions) != 0 {
		callbacks = callbackOptions[0]
	}
	if (installation != "" && !contract.ValidAuditID(installation)) || (operation != "create" && operation != "replace" && operation != "export") || clock == nil || entropy == nil {
		return nil, httpca.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return nil, err
	}
	mutated := false
	defer func() {
		err = errors.Join(err, owner.Close())
		if err != nil && mutated {
			err = &CAEffectError{Effect: "changed", Cause: err}
		}
	}()
	var inspection httpca.Inspection
	snapshot, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		var err error
		inspection, err = httpca.InspectTx(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if snapshot.Marked {
		return nil, storage.ErrStorageLatched
	}
	if installation != "" && snapshot.Identity.InstallationID != installation {
		return nil, ErrCAIdentity
	}
	installation = snapshot.Identity.InstallationID
	if operation == "create" && (inspection.Revision != "0" || inspection.Unsettled) {
		return nil, httpca.ErrUnavailable
	}
	if operation == "export" {
		if !inspection.Present {
			return nil, httpca.ErrUnavailable
		}
		certificate = inspection.Certificate
		if err == nil && callbacks.Publish != nil {
			if publishErr := callbacks.Publish(certificate, nil); publishErr != nil {
				err = errors.Join(ErrCAPublication, publishErr)
			}
		}
		return certificate, err
	}
	if callbacks.Validate != nil {
		if err = callbacks.Validate(inspection.Certificate); err != nil {
			return nil, err
		}
	}
	if callbacks.Confirm != nil {
		if err = callbacks.Confirm(CAPlan{Root: owner.Layout().Root, InstallationID: installation, Revision: inspection.Revision, HadCA: inspection.Present}); err != nil {
			return nil, err
		}
	}
	if callbacks.Validate != nil {
		if err = callbacks.Validate(inspection.Certificate); err != nil {
			return nil, err
		}
	}
	if err = snapshot.Revalidate(ctx, owner); err != nil {
		return nil, err
	}
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if err = httpca.ValidateStartup(ctx, store); err != nil {
		return nil, err
	}
	provider, err := providerFactory(installation)
	if err != nil {
		return nil, err
	}
	coordinator := keyring.NewCoordinator(provider, store, clock, entropy)
	defer coordinator.Drain()
	service, err := httpca.New(store, coordinator, installation, clock, entropy)
	if err != nil {
		return nil, err
	}
	defer service.Close()
	revision, err := service.Revision(ctx)
	if err != nil {
		return nil, err
	}
	if operation == "create" && revision != "0" {
		return nil, httpca.ErrUnavailable
	}
	// The installation lock spans this precondition, cutover and storage closure.
	// No implicit retry follows failure: authority may already have been fenced.
	if err = service.Replace(ctx, revision); err != nil {
		var cutover *httpca.CutoverError
		if errors.As(err, &cutover) {
			return nil, &CAEffectError{Effect: "uncertain", Cause: err}
		}
		return nil, err
	}
	mutated = true
	certificate, _, err = service.PublicCertificate(ctx)
	if err == nil && callbacks.Publish != nil {
		if publishErr := callbacks.Publish(certificate, inspection.Certificate); publishErr != nil {
			err = errors.Join(ErrCAPublication, publishErr)
		}
	}
	return certificate, err
}
