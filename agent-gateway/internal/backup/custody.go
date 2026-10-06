package backup

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrEncryptedCustodyUnsupported = errors.New("backup creation and restore are not supported with encrypted secret custody; preserve existing artifacts")

func requireLegacyCustody(ctx context.Context, tx *sql.Tx) error {
	enabled, err := keyring.DatabaseCustodyTx(ctx, tx)
	if err != nil {
		return err
	}
	if enabled {
		return ErrEncryptedCustodyUnsupported
	}
	return nil
}

func requireRestoreCustody(ctx context.Context, owner *gatewaypaths.Ownership, id string) error {
	if _, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error { return requireLegacyCustody(ctx, tx) }); err != nil {
		return err
	}
	path := filepath.Join(owner.Layout().Backups, id, databaseFile)
	if err := storage.RequireClosedGeneration(path); err != nil {
		return err
	}
	return storage.ViewBackup(ctx, path, func(tx *sql.Tx) error { return requireLegacyCustody(ctx, tx) })
}
