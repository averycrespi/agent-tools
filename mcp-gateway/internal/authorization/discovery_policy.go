package authorization

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type StructuralGrant struct {
	Effect       contract.GrantEffect
	ServerID     string
	UpstreamName *string
	Constrained  bool
}

type DiscoveryPolicy struct {
	Binding               CredentialBinding
	AuthorizationRevision string
	EvaluatedAt           time.Time
	Grants                []StructuralGrant
}

func (repository *Repository) LoadDiscoveryPolicy(ctx context.Context, lease *Lease, evaluatedAt time.Time) (DiscoveryPolicy, error) {
	if lease == nil || lease.owner != repository.authority || evaluatedAt.IsZero() {
		return DiscoveryPolicy{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return DiscoveryPolicy{}, err
	}
	if !lease.Current() {
		return DiscoveryPolicy{}, ErrAuthenticationRequired
	}
	evaluatedAt = evaluatedAt.UTC()
	view := DiscoveryPolicy{Binding: lease.binding, EvaluatedAt: evaluatedAt}
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		if err := verifyCurrentBindingTx(ctx, transaction, lease.binding); err != nil {
			return err
		}
		revision, err := authorizationRevisionTx(ctx, transaction)
		if err != nil {
			return err
		}
		view.AuthorizationRevision = revision
		grants, err := repository.loadStructuralGrants(ctx, transaction, lease.binding.PrincipalID, revision, evaluatedAt)
		if err != nil {
			return err
		}
		view.Grants = grants
		return nil
	})
	if err != nil {
		return DiscoveryPolicy{}, err
	}
	if !lease.Current() {
		return DiscoveryPolicy{}, ErrAuthenticationRequired
	}
	return view, nil
}

func (repository *Repository) loadStructuralGrants(ctx context.Context, transaction *sql.Tx, principalID, revision string, evaluatedAt time.Time) ([]StructuralGrant, error) {
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, principal_id, effect, server_id, upstream_name,
		       constraint_json, expires_at, created_at
		FROM grants
		WHERE principal_id = ?
		ORDER BY id
		LIMIT ?`, principalID, mustLimit("grants")+1)
	if err != nil {
		return nil, fmt.Errorf("%w: read grants for discovery: %w", ErrStorageUnavailable, err)
	}
	defer func() { _ = rows.Close() }()
	grants := make([]StructuralGrant, 0)
	count := int64(0)
	for rows.Next() {
		if count >= mustLimit("grants") {
			return nil, ErrAuthorizationUnavailable
		}
		count++
		grant, loadErr := loadEvaluationGrant(rows, func(source string) (CompiledConstraint, error) {
			return repository.compileConstraint(revision, source)
		})
		if loadErr != nil {
			return nil, ErrAuthorizationUnavailable
		}
		if grant.expiresAt != nil && !grant.expiresAt.After(evaluatedAt) {
			continue
		}
		structural := StructuralGrant{
			Effect: grant.effect, ServerID: grant.serverID, Constrained: grant.constraint != nil,
		}
		if grant.upstreamName.Valid {
			upstreamName := grant.upstreamName.String
			structural.UpstreamName = &upstreamName
		}
		grants = append(grants, structural)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate grants for discovery: %w", ErrStorageUnavailable, err)
	}
	return grants, nil
}
