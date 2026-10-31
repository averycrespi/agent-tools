package composition

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

func initializeConfiguration(ctx context.Context, owner *gatewaypaths.Ownership, input configimport.Input, clock Clock, entropy io.Reader, fault func(string) error) (err error) {
	ctx = audit.WithOffline(ctx)
	var store *storage.Store
	var openErr error
	if fault == nil {
		store, openErr = storage.Open(ctx, owner)
	} else {
		store, openErr = storage.OpenWithFaultInjection(ctx, owner, func(point storage.FaultPoint) error { return fault("import-marker:" + string(point)) })
	}
	err = openErr
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	var digest string
	if err := store.View(ctx, func(tx *sql.Tx) error { var err error; digest, err = configimport.ReceiptTx(ctx, tx); return err }); err != nil {
		return err
	}
	if digest != "" {
		if digest != input.Digest {
			return storage.ErrPlanChanged
		}
		if err := store.View(ctx, func(tx *sql.Tx) error { return configimport.VerifyReceiptTx(ctx, tx, input.Digest) }); err != nil {
			return err
		}
		return store.Checkpoint(ctx)
	}
	targets, err := servers.New(store, clock, entropy)
	if err != nil {
		return err
	}
	if err := store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := configimport.StageTx(ctx, tx, input, func() (string, error) { return admin.NewID(clock.Now(), entropy) }, clock.Now(), targets); err != nil {
			return err
		}
		if fault != nil {
			return fault("import-transaction-staged")
		}
		return nil
	}); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("import-transaction-committed"); err != nil {
			return err
		}
	}
	return store.Checkpoint(ctx)
}
