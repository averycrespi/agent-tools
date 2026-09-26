package main

import (
	"context"
	"crypto/rand"
	"io"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/spf13/cobra"
)

type offlineBackend map[string]string

func (offlineBackend) Probe(context.Context, string) error { return nil }
func (b offlineBackend) Set(service, user, value string) error {
	b[service+"/"+user] = value
	return nil
}
func (b offlineBackend) Get(service, user string) (string, error) {
	value, ok := b[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (b offlineBackend) Delete(service, user string) error { delete(b, service+"/"+user); return nil }

// CLI tests use actual CA SQL and protected-generation lifecycle with a disposable
// in-memory backend. Composition's own tests independently cover the stopped owner.
func newTestRootCmd(t *testing.T) *cobra.Command {
	t.Helper()
	return newRootCmdWithDependencies(offlineDependencies{clock: systemClock{}, entropy: rand.Reader, newComposition: composition.New, caOperation: fixtureCAOperation})
}

func fixtureCAOperation(ctx context.Context, root, installation, operation string, clock composition.Clock, entropy io.Reader, callbacks composition.CACallbacks) ([]byte, error) {
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = owner.Close() }()
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	identity, err := store.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if installation != "" && installation != identity.InstallationID {
		return nil, composition.ErrCAIdentity
	}
	inspection, err := httpca.Inspect(ctx, store)
	if err != nil {
		return nil, err
	}
	if operation != "export" {
		if callbacks.Validate != nil {
			if err := callbacks.Validate(inspection.Certificate); err != nil {
				return nil, err
			}
		}
		if callbacks.Confirm != nil {
			if err := callbacks.Confirm(composition.CAPlan{Root: root, InstallationID: identity.InstallationID, Revision: inspection.Revision, HadCA: inspection.Present}); err != nil {
				return nil, err
			}
		}
		if operation == "create" && inspection.Revision != "0" {
			return nil, httpca.ErrUnavailable
		}
		provider, err := keyring.NewProviderWithBackend(identity.InstallationID, offlineBackend{})
		if err != nil {
			return nil, err
		}
		coordinator := keyring.NewCoordinator(provider, store, clock, entropy)
		defer coordinator.Drain()
		ca, err := httpca.New(store, coordinator, identity.InstallationID, clock, entropy)
		if err != nil {
			return nil, err
		}
		defer ca.Close()
		if err := ca.Replace(ctx, inspection.Revision); err != nil {
			return nil, err
		}
	}
	cert, _, err := httpca.PublicCertificate(ctx, store)
	if err != nil {
		return nil, err
	}
	if callbacks.Publish != nil {
		if err := callbacks.Publish(cert, inspection.Certificate); err != nil {
			return cert, err
		}
	}
	return cert, nil
}
