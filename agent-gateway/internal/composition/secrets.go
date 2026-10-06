package composition

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrSecretCustody = keyring.ErrCustodyUnavailable

// SetupSecrets provisions custody only; it never reads or migrates native values.
func SetupSecrets(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, clock Clock) error {
	return keyring.SetupCustody(ctx, owner, store, clock)
}

func SetupStoppedSecrets(ctx context.Context, root string, clock Clock, approval storage.StoppedApproval) (identity storage.Identity, err error) {
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return identity, err
	}
	defer func() { err = errors.Join(err, owner.Close()) }()
	if err = storage.ApproveStopped(ctx, owner, []storage.StoppedApproval{approval}); err != nil {
		return identity, err
	}
	store, err := storage.Open(ctx, owner)
	if err != nil {
		return identity, err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if err = SetupSecrets(ctx, owner, store, clock); err != nil {
		return identity, err
	}
	return store.Identity(ctx)
}
