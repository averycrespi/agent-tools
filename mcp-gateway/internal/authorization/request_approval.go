package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

// GrantRequestApproval is the purpose-specific request-owned half of one approval transaction.
type GrantRequestApproval interface {
	PrepareGrantRequestApproval(context.Context, *sql.Tx) (ApprovalGrantMaterial, error)
	CommitGrantRequestApproval(context.Context, *sql.Tx, string, time.Time) (contract.AgentGrantRequest, error)
}

// ApprovalGrantMaterial is the closed ordinary ALLOW material prepared from one request.
type ApprovalGrantMaterial struct {
	Description     *string
	PrincipalID     string
	ServerID        string
	UpstreamName    *string
	Constraint      *json.RawMessage
	DurationSeconds *int64
}

// GrantRequestApprovalResult is one atomically created grant and approved request.
type GrantRequestApprovalResult struct {
	Grant   contract.Grant
	Request contract.AgentGrantRequest
}

// ApproveGrantRequest owns authority admission, one mutation, one grant, and one revision advance.
func (repository *Repository) ApproveGrantRequest(ctx context.Context, transition GrantRequestApproval) (GrantRequestApprovalResult, error) {
	if transition == nil {
		return GrantRequestApprovalResult{}, ErrInvalidInput
	}
	releaseGate, err := repository.authority.tryAcquire(ctx)
	if err != nil {
		if errors.Is(err, ErrResourceLimit) {
			return GrantRequestApprovalResult{}, ErrApprovalUnavailable
		}
		return GrantRequestApprovalResult{}, err
	}
	defer releaseGate()

	var result GrantRequestApprovalResult
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		material, prepareErr := transition.PrepareGrantRequestApproval(ctx, transaction)
		if prepareErr != nil {
			return prepareErr
		}
		constraintJSON, validateErr := validateApprovalGrantMaterial(material)
		if validateErr != nil {
			return validateErr
		}
		if _, principalErr := principalByIDTx(ctx, transaction, material.PrincipalID); principalErr != nil {
			if errors.Is(principalErr, sql.ErrNoRows) {
				return ErrInvalidState
			}
			return principalErr
		}
		var grants int64
		if countErr := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants`).Scan(&grants); countErr != nil {
			return fmt.Errorf("count grants for request approval: %w", countErr)
		}
		if grants >= mustLimit("grants") {
			return ErrResourceLimit
		}

		now := repository.clock.Now().UTC()
		milliseconds := now.UnixMilli()
		if now.IsZero() || milliseconds < 0 || milliseconds > 1<<48-1 {
			return ErrIdentityUnavailable
		}
		conflict, conflictErr := repository.HasActiveDenyConflictTx(ctx, transaction, DenyConflictScope{
			PrincipalID: material.PrincipalID, ServerID: material.ServerID, UpstreamName: material.UpstreamName,
		}, now)
		if conflictErr != nil {
			return conflictErr
		}
		if conflict {
			return ErrConflict
		}
		grantID, identityErr := repository.newID(now)
		if identityErr != nil {
			return fmt.Errorf("%w: generate request approval identity: %w", ErrIdentityUnavailable, identityErr)
		}
		var collision int
		if queryErr := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants WHERE id = ?`, grantID).Scan(&collision); queryErr != nil {
			return fmt.Errorf("inspect request approval grant identity: %w", queryErr)
		}
		if collision != 0 {
			return ErrIdentityUnavailable
		}
		var expiresAt *time.Time
		if material.DurationSeconds != nil {
			expires := now.Add(time.Duration(*material.DurationSeconds) * time.Second)
			expiresAt = &expires
		}
		if _, insertErr := transaction.ExecContext(ctx, `INSERT INTO grants (
			id, description, principal_id, effect, server_id, upstream_name,
			constraint_json, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			grantID, nullableGrantString(material.Description), material.PrincipalID, contract.GrantAllow, material.ServerID,
			nullableGrantString(material.UpstreamName), nullableGrantBytes(constraintJSON),
			nullableGrantTime(expiresAt), formatAuthorizationTime(now)); insertErr != nil {
			return fmt.Errorf("insert request approval grant: %w", insertErr)
		}
		approved, transitionErr := transition.CommitGrantRequestApproval(ctx, transaction, grantID, now)
		if transitionErr != nil {
			return transitionErr
		}
		if approved.State != contract.RequestApproved || approved.Revision != "2" || approved.ApprovedGrantID == nil || *approved.ApprovedGrantID != grantID {
			return ErrInvalidState
		}
		if revisionErr := advanceAuthorizationRevisionTx(ctx, transaction); revisionErr != nil {
			return revisionErr
		}
		_, grant, scanErr := repository.scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), now)
		if scanErr != nil {
			return scanErr
		}
		result = GrantRequestApprovalResult{Grant: grant, Request: approved}
		return audit.MutationTx(audit.WithSystem(ctx), transaction, now, "grant", "create", contract.AuditTarget{Type: "grant", ID: grantID})
	})
	if err != nil {
		return GrantRequestApprovalResult{}, repository.mapApprovalMutationError(err)
	}
	return result, nil
}

func validateApprovalGrantMaterial(material ApprovalGrantMaterial) ([]byte, error) {
	if !validGrantDescription(material.Description) || !validOpaqueID(material.PrincipalID) || !validOpaqueID(material.ServerID) ||
		material.UpstreamName != nil && !validUpstreamName(*material.UpstreamName) ||
		material.UpstreamName == nil && material.Constraint != nil {
		return nil, ErrInvalidState
	}
	if material.DurationSeconds != nil && (*material.DurationSeconds < contract.GrantRequestDurationMinimumSeconds ||
		*material.DurationSeconds > contract.GrantRequestDurationMaximumSeconds) {
		return nil, ErrInvalidState
	}
	if material.Constraint == nil {
		return nil, nil
	}
	compiled, err := CompileConstraint(*material.Constraint)
	if err != nil {
		return nil, ErrInvalidState
	}
	return compiled.JSON(), nil
}

func (repository *Repository) mapApprovalMutationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrStorageLatched) || errors.Is(err, storage.ErrMutationBusy) || repository.store.Latched() {
		return fmt.Errorf("%w: %w", ErrApprovalUnavailable, err)
	}
	return repository.mapMutationError(err)
}
