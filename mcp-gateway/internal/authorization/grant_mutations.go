package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func (repository *Repository) CreateGrant(
	ctx context.Context,
	request CreateGrantRequest,
	validateTarget CurrentGrantTargetValidator,
) (contract.Grant, error) {
	if validateTarget == nil || !validGrantDescription(request.Description) || !validOpaqueID(request.PrincipalID) || !validOpaqueID(request.ServerID) || !validGrantEffect(request.Effect) ||
		request.UpstreamName != nil && !validUpstreamName(*request.UpstreamName) || request.UpstreamName == nil && request.Constraint != nil {
		return contract.Grant{}, ErrInvalidInput
	}
	var constraintJSON []byte
	if request.Constraint != nil {
		compiled, err := CompileConstraint(*request.Constraint)
		if err != nil {
			return contract.Grant{}, ErrInvalidInput
		}
		constraintJSON = compiled.JSON()
	}
	now := repository.clock.Now().UTC()
	if request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
		return contract.Grant{}, ErrInvalidInput
	}
	grantID, err := repository.newID(now)
	if err != nil {
		return contract.Grant{}, err
	}
	createdAt := formatAuthorizationTime(now)
	var grant contract.Grant
	err = repository.mutateAuthorityTx(ctx, "", func(transaction *sql.Tx) error {
		if _, err := principalByIDTx(ctx, transaction, request.PrincipalID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidInput
			}
			return err
		}
		var grants int64
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants`).Scan(&grants); err != nil {
			return fmt.Errorf("count grants: %w", err)
		}
		if grants >= mustLimit("grants") {
			return ErrResourceLimit
		}
		valid, err := validateTarget(ctx, transaction, request.ServerID)
		if err != nil {
			return fmt.Errorf("validate current grant target: %w", err)
		}
		if !valid {
			return ErrInvalidInput
		}
		var collisions int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants WHERE id = ?`, grantID).Scan(&collisions); err != nil {
			return fmt.Errorf("inspect generated grant identity: %w", err)
		}
		if collisions != 0 {
			return ErrIdentityUnavailable
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO grants (
				id, description, principal_id, effect, server_id, upstream_name,
				constraint_json, expires_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			grantID, nullableGrantString(request.Description), request.PrincipalID, request.Effect, request.ServerID,
			nullableGrantString(request.UpstreamName), nullableGrantBytes(constraintJSON),
			nullableGrantTime(request.ExpiresAt), createdAt); err != nil {
			return fmt.Errorf("insert grant: %w", err)
		}
		if err := advanceAuthorizationRevisionTx(ctx, transaction); err != nil {
			return err
		}
		_, grant, err = scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), now)
		if err != nil {
			return err
		}
		return audit.MutationTx(ctx, transaction, now, "grant", "create", contract.AuditTarget{Type: "grant", ID: grantID})
	})
	return grant, repository.mapMutationError(err)
}

func (repository *Repository) PatchGrant(ctx context.Context, grantID string, request PatchGrantRequest) (contract.Grant, error) {
	if !validOpaqueID(grantID) || request.Description == nil || !validGrantDescription(*request.Description) || !validRevision(request.ExpectedRevision) {
		return contract.Grant{}, ErrInvalidInput
	}
	var updated contract.Grant
	err := repository.mutateAuthorityTx(ctx, "", func(transaction *sql.Tx) error {
		_, current, err := scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), repository.clock.Now())
		if err != nil {
			return err
		}
		if current.Revision != request.ExpectedRevision {
			return ErrStaleRevision
		}
		if equalOptionalString(current.Description, *request.Description) {
			return ErrConflict
		}
		revision, err := strconv.ParseUint(current.Revision, 10, 64)
		if err != nil || revision == math.MaxUint64 {
			return ErrInvalidState
		}
		result, err := transaction.ExecContext(ctx, `UPDATE grants SET description = ?, revision = ? WHERE id = ? AND revision = ?`,
			nullableGrantString(*request.Description), revision+1, grantID, revision)
		if err != nil {
			return fmt.Errorf("update grant description: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect grant description update: %w", err)
		}
		if changed != 1 {
			return ErrStaleRevision
		}
		_, updated, err = scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), repository.clock.Now())
		if err != nil {
			return err
		}
		return audit.MutationTx(ctx, transaction, repository.clock.Now(), "grant", "update", contract.AuditTarget{Type: "grant", ID: grantID})
	})
	return updated, repository.mapMutationError(err)
}

func equalOptionalString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (repository *Repository) DeleteGrant(ctx context.Context, grantID string) error {
	if !validOpaqueID(grantID) {
		return ErrNotFound
	}
	err := repository.mutateAuthorityTx(ctx, "", func(transaction *sql.Tx) error {
		if _, _, err := scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), repository.clock.Now()); err != nil {
			return err
		}
		result, err := transaction.ExecContext(ctx, `DELETE FROM grants WHERE id = ?`, grantID)
		if err != nil {
			return fmt.Errorf("delete grant: %w", err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect grant deletion: %w", err)
		}
		if deleted != 1 {
			return ErrNotFound
		}
		if err := advanceAuthorizationRevisionTx(ctx, transaction); err != nil {
			return err
		}
		return audit.MutationTx(ctx, transaction, repository.clock.Now(), "grant", "delete", contract.AuditTarget{Type: "grant", ID: grantID})
	})
	return repository.mapMutationError(err)
}

func validGrantEffect(effect contract.GrantEffect) bool {
	return effect == contract.GrantAllow || effect == contract.GrantDeny
}

func nullableGrantString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableGrantBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return string(value)
}

func nullableGrantTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatAuthorizationTime(*value)
}
