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

func rewrapRestore(ctx context.Context, owner *gatewaypaths.Ownership, replacement *storage.Store, recoveryKey, oldID string, target keyring.BackupCustody) error {
	old, err := gatewaypaths.ReadRecoveryKey(recoveryKey)
	if err != nil {
		return keyring.ErrCustodyUnavailable
	}
	defer clear(old)
	if err := keyring.VerifyRecoveryKey(old, oldID); err != nil {
		return err
	}
	active, err := gatewaypaths.MasterKey(owner, nil)
	if err != nil {
		return keyring.ErrCustodyUnavailable
	}
	defer clear(active)
	if err := keyring.VerifyRecoveryKey(active, target.KeyID); err != nil {
		return err
	}
	return keyring.RewrapCustody(ctx, replacement, old, active, target.Encryptions, nil)
}

type restoreCustody struct {
	Current     keyring.BackupCustody
	RewrapCount int64
	CrossKey    bool
}

func requireRestoreCustody(ctx context.Context, owner *gatewaypaths.Ownership, artifact artifactMetadata, recoveryKey string) (restoreCustody, error) {
	var result restoreCustody
	if err := gatewaypaths.RequireNoKeyRotation(owner); err != nil {
		return result, err
	}
	var current keyring.BackupCustody
	var encrypted bool
	snapshot, err := storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
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
	})
	if err != nil {
		return result, err
	}
	result.Current = current
	result.CrossKey = artifact.Format == 4 && current.KeyID != artifact.MasterKeyID
	if result.CrossKey && snapshot.Marked {
		return result, storage.ErrStorageLatched
	}
	if recoveryKey != "" && !result.CrossKey {
		return result, ErrEncryptedCustodyUnsupported
	}
	path := filepath.Join(owner.Layout().Backups, artifact.ID, databaseFile)
	if err := storage.RequireClosedGeneration(path); err != nil {
		return result, err
	}
	err = storage.ViewBackup(ctx, path, func(tx *sql.Tx) error {
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
		if !encrypted || !selected {
			return ErrEncryptedCustodyUnsupported
		}
		active, err := gatewaypaths.MasterKey(owner, nil)
		if err != nil {
			return keyring.ErrCustodyUnavailable
		}
		defer clear(active)
		if err := keyring.VerifyRecoveryKey(active, current.KeyID); err != nil {
			return err
		}
		if result.CrossKey {
			if recoveryKey == "" {
				return ErrEncryptedCustodyUnsupported
			}
			old, err := gatewaypaths.ReadRecoveryKey(recoveryKey)
			if err != nil {
				return keyring.ErrCustodyUnavailable
			}
			defer clear(old)
			if err := keyring.VerifyRecoveryKey(old, artifact.MasterKeyID); err != nil {
				return err
			}
			if _, err := keyring.VerifyRecoveryCustodyTx(ctx, tx, old, func(kind keyring.RecordKind, handle keyring.Handle, payload []byte) error {
				if kind == keyring.RecordHTTPCA {
					return httpca.VerifyBackupMaterialTx(ctx, tx, string(handle), payload)
				}
				return nil
			}); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM secret_generations`).Scan(&result.RewrapCount); err != nil {
				return err
			}
			if result.RewrapCount > 4294967296-current.Encryptions {
				return keyring.ErrCustodyUnavailable
			}
			return nil
		}
		recovered, err := verifyEncryptedCustodyTx(ctx, tx, owner)
		if err != nil {
			return err
		}
		if recovered.KeyID != current.KeyID {
			return keyring.ErrCustodyUnavailable
		}
		result.Current.Encryptions = max(current.Encryptions, recovered.Encryptions)
		return nil
	})
	return result, err
}
