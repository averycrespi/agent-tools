package storage

import (
	"context"
	"errors"
	"fmt"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
)

func VerifyCurrent(ctx context.Context, root string) (Identity, error) {
	ownership, err := gatewaypaths.AcquireForMaintenance(root)
	if err != nil {
		if errors.Is(err, gatewaypaths.ErrInUse) {
			return Identity{}, fmt.Errorf("verify-current requires a stopped Gateway: %w", err)
		}
		return Identity{}, fmt.Errorf("acquire stopped-process ownership: %w", err)
	}
	defer func() { _ = ownership.Close() }()
	layout := ownership.Layout()
	if err := gatewaypaths.ValidateOwnerOnlyFile(layout.Database); err != nil {
		return Identity{}, fmt.Errorf("%w: database path: %w", ErrInvalidDatabase, err)
	}
	version, err := inspectDatabase(ctx, layout.Database)
	if err != nil {
		return Identity{}, err
	}
	if version != CurrentSchema {
		return Identity{}, fmt.Errorf("%w: verify-current requires schema %d, found %d", ErrInvalidDatabase, CurrentSchema, version)
	}
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return Identity{}, err
	}
	if err := store.verify(ctx); err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	overLimit, err := store.overDatabaseLimit(ctx)
	if err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	if overLimit {
		_ = store.Close()
		return Identity{}, fmt.Errorf("%w: database exceeds compiled size limit", ErrStorageLatched)
	}
	identity, err := store.Identity(ctx)
	if err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	marker := newMutationMarker(layout, nil)
	recovery, err := marker.recovery(identity.InstallationID)
	if err != nil {
		_ = store.Close()
		return Identity{}, fmt.Errorf("%w: inspect mutation recovery action: %w", ErrStorageLatched, err)
	}
	if recovery != nil {
		if err := restoreKeyringAuthorityFence(ctx, store, recovery); err != nil {
			_ = store.Close()
			return Identity{}, fmt.Errorf("%w: restore keyring authority fence: %w", ErrStorageLatched, err)
		}
		identity, err = store.Identity(ctx)
		if err != nil {
			_ = store.Close()
			return Identity{}, err
		}
	}
	if err := store.Close(); err != nil {
		return Identity{}, err
	}
	if err := marker.clearVerified(identity.InstallationID); err != nil {
		return Identity{}, fmt.Errorf("%w: clear verified mutation marker: %w", ErrStorageLatched, err)
	}
	if err := ownership.MarkClean(); err != nil {
		return Identity{}, fmt.Errorf("mark verified maintenance clean: %w", err)
	}
	return identity, nil
}

func restoreKeyringAuthorityFence(
	ctx context.Context,
	store *Store,
	recovery *keyringFenceActivation,
) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT OR IGNORE INTO keyring_authority_fences (owner, kind)
		VALUES (?, ?)`, recovery.Owner, recovery.Kind); err != nil {
		_ = transaction.Rollback()
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`); err != nil {
		_ = transaction.Rollback()
		return err
	}
	return transaction.Commit()
}
