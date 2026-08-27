package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type DenyConflictScope struct {
	PrincipalID  string
	ServerID     string
	UpstreamName *string
}

func (repository *Repository) HasActiveDenyConflictTx(
	ctx context.Context,
	transaction *sql.Tx,
	scope DenyConflictScope,
	evaluatedAt time.Time,
) (bool, error) {
	if transaction == nil || !validOpaqueID(scope.PrincipalID) || !validOpaqueID(scope.ServerID) || evaluatedAt.IsZero() ||
		scope.UpstreamName != nil && !validUpstreamName(*scope.UpstreamName) {
		return false, ErrInvalidInput
	}
	if repository.store.Latched() {
		return false, ErrStorageUnavailable
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, principal_id, effect, server_id, upstream_name,
		       constraint_json, expires_at, created_at
		FROM grants
		WHERE principal_id = ? AND effect = ? AND server_id = ?
		ORDER BY id
		LIMIT ?`, scope.PrincipalID, contract.GrantDeny, scope.ServerID, mustLimit("grants")+1)
	if err != nil {
		return false, fmt.Errorf("%w: read DENY conflicts: %w", ErrStorageUnavailable, err)
	}
	defer func() { _ = rows.Close() }()
	conflict := false
	count := int64(0)
	evaluatedAt = evaluatedAt.UTC()
	for rows.Next() {
		if count >= mustLimit("grants") {
			return false, ErrAuthorizationUnavailable
		}
		count++
		grant, loadErr := loadEvaluationGrant(rows)
		if loadErr != nil || grant.effect != contract.GrantDeny {
			return false, ErrAuthorizationUnavailable
		}
		if grant.expiresAt != nil && !grant.expiresAt.After(evaluatedAt) {
			continue
		}
		if scope.UpstreamName == nil || !grant.upstreamName.Valid || grant.upstreamName.String == *scope.UpstreamName {
			conflict = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("%w: iterate DENY conflicts: %w", ErrStorageUnavailable, err)
	}
	if repository.store.Latched() {
		return false, ErrStorageUnavailable
	}
	return conflict, nil
}
