package authorization

import (
	"context"
	"database/sql"
)

// AcquireGitCredentialAuthority borrows the existing shared authority gate for
// one credential transaction, before any storage or coordinator-state lock.
// The caller must release it before provider I/O and never acquire it in SQL.
func (r *Repository) AcquireGitCredentialAuthority(ctx context.Context) (func(), error) {
	return r.authority.acquire(ctx)
}

// AdvanceGitCredentialRevisionTx is the credential owner's supplied-transaction
// seam. Fence, publication, activation and invalidation advance shared policy
// evidence atomically; it opens no nested storage mutation.
func AdvanceGitCredentialRevisionTx(ctx context.Context, tx *sql.Tx) error {
	return advanceAuthorizationRevisionTx(ctx, tx)
}
