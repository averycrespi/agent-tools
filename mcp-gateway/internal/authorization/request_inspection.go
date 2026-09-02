package authorization

import (
	"context"
	"database/sql"
	"fmt"
)

// StoredPrincipalExistsTx reports whether a permanent principal identity exists on the supplied transaction.
func (repository *Repository) StoredPrincipalExistsTx(ctx context.Context, transaction *sql.Tx, principalID string) (bool, error) {
	if !validOpaqueID(principalID) {
		return false, ErrInvalidInput
	}
	if transaction == nil {
		return false, fmt.Errorf("%w: stored-principal transaction is unavailable", ErrStorageUnavailable)
	}
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM principals WHERE id = ?`, principalID).Scan(&count); err != nil {
		return false, fmt.Errorf("%w: inspect stored principal: %w", ErrStorageUnavailable, err)
	}
	if count < 0 || count > 1 {
		return false, ErrAuthorizationUnavailable
	}
	return count == 1, nil
}
