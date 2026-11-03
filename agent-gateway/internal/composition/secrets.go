package composition

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrSecretCustody = keyring.ErrCustodyUnavailable

// RotateMasterKey never constructs providers or consults native credentials.
func RotateMasterKey(ctx context.Context, root string, clock Clock, recoverPending bool, approval storage.StoppedApproval) (result keyring.RotationResult, err error) {
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, owner.Close()) }()
	return keyring.RotateStoppedMasterKey(ctx, owner, clock, recoverPending, approval)
}

// SetupSecrets provisions custody only; it never reads or migrates native values.
func SetupSecrets(ctx context.Context, owner *gatewaypaths.Ownership, store *storage.Store, clock Clock) error {
	return keyring.SetupCustody(ctx, owner, store, clock)
}
