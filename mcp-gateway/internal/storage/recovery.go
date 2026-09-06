package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
)

func VerifyCurrent(ctx context.Context, root string) (Identity, error) {
	ctx = audit.WithOffline(ctx)
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
	attempt, err := audit.NewAttempt(ctx, time.Now(), "storage", "verify", contract.AuditTarget{Type: "installation", ID: identity.InstallationID})
	if err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	ctx = audit.WithCorrelation(ctx, attempt.CorrelationID)
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	if _, err := audit.AppendTx(ctx, transaction, attempt); err != nil {
		_ = transaction.Rollback()
		_ = store.Close()
		return Identity{}, err
	}
	if err := transaction.Commit(); err != nil {
		_ = store.Close()
		return Identity{}, err
	}
	if recovery != nil {
		switch recovery.Action {
		case "restore_keyring_authority_fence":
			err = restoreKeyringAuthorityFence(ctx, store, recovery)
		case "invalidate_agent_credential_candidate":
			err = invalidateAgentCredentialCandidate(ctx, store, recovery)
		default:
			err = fmt.Errorf("unsupported recovery action")
		}
		if err != nil {
			_ = store.Close()
			return Identity{}, fmt.Errorf("%w: apply stopped recovery action: %w", ErrStorageLatched, err)
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
	settled, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return Identity{}, err
	}
	evidenceErr := audit.Finish(ctx, settled, attempt, time.Now(), "succeeded")
	if err := errors.Join(evidenceErr, settled.Close()); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func restoreKeyringAuthorityFence(
	ctx context.Context,
	store *Store,
	recovery *recoveryAction,
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
	if err := audit.MutationTx(ctx, transaction, time.Now(), "keyring", "fence", contract.AuditTarget{Type: "server", ID: recovery.Owner}); err != nil {
		_ = transaction.Rollback()
		return err
	}
	return transaction.Commit()
}

func invalidateAgentCredentialCandidate(
	ctx context.Context,
	store *Store,
	recovery *recoveryAction,
) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE principals
		SET revision = revision + 1,
		    credential_revision = credential_revision + 1,
		    credential_id = NULL,
		    credential_verifier = NULL,
		    credential_fingerprint = NULL,
		    credential_created_at = NULL
		WHERE id = ?
		  AND credential_id = ?
		  AND revision = ?
		  AND credential_revision = ?`,
		recovery.PrincipalID, recovery.CredentialID,
		recovery.PrincipalRevision, recovery.CredentialRevision)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed < 0 || changed > 1 {
		_ = transaction.Rollback()
		if err != nil {
			return err
		}
		return fmt.Errorf("agent credential recovery changed an invalid row count")
	}
	if changed == 1 {
		if err := audit.MutationTx(ctx, transaction, time.Now(), "agent_credential", "invalidate", contract.AuditTarget{Type: "agent_credential", ID: recovery.CredentialID}); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	return transaction.Commit()
}
