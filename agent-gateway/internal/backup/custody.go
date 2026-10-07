package backup

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var ErrEncryptedCustodyUnsupported = errors.New("backup format or legacy dependencies cannot recover encrypted custody; use a complete format-4 backup and its matching master key")

func verifyEncryptedCustodyTx(ctx context.Context, tx *sql.Tx, owner *gatewaypaths.Ownership) (keyring.BackupCustody, error) {
	return keyring.VerifyBackupCustodyTx(ctx, tx, owner, func(kind keyring.RecordKind, handle keyring.Handle, payload []byte) error {
		if kind == keyring.RecordHTTPCA {
			return httpca.VerifyBackupMaterialTx(ctx, tx, string(handle), payload)
		}
		return nil
	})
}

func verifyEncryptedDomainsTx(ctx context.Context, tx *sql.Tx) error {
	for _, verify := range []func(context.Context, *sql.Tx) error{servers.VerifyBackupCredentialsTx, httpcredentials.VerifyBackupTx, gitcredentials.VerifyEncryptedBackupTx, httpca.VerifyBackupTx} {
		if err := verify(ctx, tx); err != nil {
			return errors.Join(ErrInvalidArtifact, err)
		}
	}
	return nil
}

func requireBackupCustody(ctx context.Context, tx *sql.Tx) error {
	enabled, err := keyring.DatabaseCustodyTx(ctx, tx)
	if err != nil || !enabled {
		return err
	}
	_, err = keyring.InspectBackupCustodyTx(ctx, tx)
	if err != nil {
		return errors.Join(ErrEncryptedCustodyUnsupported, err)
	}
	return nil
}

func requireRestoreCustody(ctx context.Context, owner *gatewaypaths.Ownership, artifact artifactMetadata) (keyring.BackupCustody, error) {
	var current keyring.BackupCustody
	var encrypted bool
	if _, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
		var err error
		encrypted, err = keyring.DatabaseCustodyTx(ctx, tx)
		if err != nil {
			return err
		}
		if encrypted {
			// Current rows may be damaged or legacy; only the nonrefundable same-key
			// high-water is needed. Never fall back to a backup's counter.
			current, err = keyring.RestoreHighWaterTx(ctx, tx)
		}
		return err
	}); err != nil {
		return current, err
	}
	path := filepath.Join(owner.Layout().Backups, artifact.ID, databaseFile)
	if err := storage.RequireClosedGeneration(path); err != nil {
		return current, err
	}
	err := storage.ViewBackup(ctx, path, func(tx *sql.Tx) error {
		selected, err := keyring.DatabaseCustodyTx(ctx, tx)
		if err != nil {
			return err
		}
		if artifact.Format != 4 {
			if encrypted || selected {
				return ErrEncryptedCustodyUnsupported
			}
			return nil
		}
		if !encrypted || !selected || current.KeyID != artifact.MasterKeyID {
			return ErrEncryptedCustodyUnsupported
		}
		recovered, err := verifyEncryptedCustodyTx(ctx, tx, owner)
		if err != nil {
			return err
		}
		if recovered.KeyID != current.KeyID {
			return keyring.ErrCustodyUnavailable
		}
		current.Encryptions = max(current.Encryptions, recovered.Encryptions)
		return nil
	})
	return current, err
}
