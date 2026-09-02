package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

func InvalidateStagedCredentials(ctx context.Context, staged *storage.Store, targets StoredGrantTargetInspector) error {
	if staged == nil || targets == nil {
		return ErrInvalidInput
	}
	err := staged.Mutate(ctx, func(transaction *sql.Tx) error {
		if err := validateAuthorityTx(ctx, transaction, targets); err != nil {
			return err
		}
		var credentialCount, exhausted int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*), coalesce(sum(CASE WHEN revision >= ? OR credential_revision >= ? THEN 1 ELSE 0 END), 0)
			FROM principals
			WHERE credential_id IS NOT NULL`, math.MaxInt64, math.MaxInt64).Scan(&credentialCount, &exhausted); err != nil {
			return fmt.Errorf("inspect staged credential revisions: %w", err)
		}
		if credentialCount < 0 || credentialCount > mustLimit("principals") || exhausted != 0 {
			return errorsInvalidState("staged credential revisions cannot advance")
		}
		result, err := transaction.ExecContext(ctx, `
			UPDATE principals
			SET revision = revision + 1,
			    credential_revision = credential_revision + 1,
			    credential_id = NULL,
			    credential_verifier = NULL,
			    credential_fingerprint = NULL,
			    credential_created_at = NULL
			WHERE credential_id IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("invalidate staged credentials: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect staged credential invalidation: %w", err)
		}
		if changed != credentialCount {
			return errorsInvalidState("staged credential invalidation changed an invalid row count")
		}
		if err := validateAuthorityTx(ctx, transaction, targets); err != nil {
			return err
		}
		var remaining int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM principals
			WHERE credential_id IS NOT NULL
			   OR credential_verifier IS NOT NULL
			   OR credential_fingerprint IS NOT NULL
			   OR credential_created_at IS NOT NULL`).Scan(&remaining); err != nil {
			return fmt.Errorf("verify staged credential invalidation: %w", err)
		}
		if remaining != 0 {
			return errorsInvalidState("staged credential invalidation left current authority")
		}
		return nil
	})
	return mapStagedInvalidationError(err)
}

func mapStagedInvalidationError(err error) error {
	if err == nil || isRepositoryError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, storage.ErrMutationBusy) {
		return fmt.Errorf("%w: %w", ErrResourceLimit, err)
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}
