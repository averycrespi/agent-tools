package composition

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// HTTPCA operates only on a locked, existing stopped installation. It never
// constructs the serving graph, opens listeners, or changes client trust.
func HTTPCA(ctx context.Context, root, installation, operation string, clock Clock, entropy io.Reader) ([]byte, error) {
	return httpCA(ctx, root, installation, operation, clock, entropy, productionProvider)
}

func httpCA(ctx context.Context, root, installation, operation string, clock Clock, entropy io.Reader, providerFactory func(string) (*keyring.Provider, error)) (certificate []byte, err error) {
	if !contract.ValidAuditID(installation) || (operation != "create" && operation != "replace" && operation != "export") || clock == nil || entropy == nil {
		return nil, httpca.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, owner.Close()) }()
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, store.Close())
		if err != nil {
			certificate = nil
		}
	}()
	identity, err := store.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.InstallationID != installation || store.Latched() {
		return nil, httpca.ErrUnavailable
	}
	if err = httpca.ValidateStartup(ctx, store); err != nil {
		return nil, err
	}
	if operation == "export" {
		certificate, _, err = httpca.PublicCertificate(ctx, store)
		return certificate, err
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
	if (operation == "create") != (revision == "0") {
		return nil, httpca.ErrUnavailable
	}
	// The installation lock spans this precondition, cutover and storage closure.
	// No implicit retry follows failure: authority may already have been fenced.
	if err = service.Replace(ctx, revision); err != nil {
		return nil, err
	}
	certificate, _, err = service.PublicCertificate(ctx)
	return certificate, err
}
