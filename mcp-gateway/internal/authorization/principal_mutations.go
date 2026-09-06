package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

func (repository *Repository) CreatePrincipal(ctx context.Context, request CreatePrincipalRequest) (contract.PrincipalCreation, error) {
	if !validDisplayName(request.DisplayName) || !validVisibility(request.Visibility) {
		return contract.PrincipalCreation{}, ErrInvalidInput
	}
	now := repository.clock.Now().UTC()
	principalID, err := repository.newID(now)
	if err != nil {
		return contract.PrincipalCreation{}, err
	}
	grantID, err := repository.newID(now)
	if err != nil {
		return contract.PrincipalCreation{}, err
	}
	if principalID == grantID {
		return contract.PrincipalCreation{}, ErrIdentityUnavailable
	}
	timestamp := formatAuthorizationTime(now)
	var creation contract.PrincipalCreation
	err = repository.mutateAuthorityTx(ctx, "", func(transaction *sql.Tx) error {
		if err := checkPrincipalCreationCapacity(ctx, transaction); err != nil {
			return err
		}
		var collisions int
		if err := transaction.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM principals WHERE id = ?) +
			       (SELECT count(*) FROM grants WHERE id = ?)`, principalID, grantID).Scan(&collisions); err != nil {
			return fmt.Errorf("inspect generated authorization identities: %w", err)
		}
		if collisions != 0 {
			return ErrIdentityUnavailable
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO principals (
				id, display_name, state, visibility, revision, credential_revision,
				created_at, updated_at
			) VALUES (?, ?, 'active', ?, 1, 0, ?, ?)`,
			principalID, request.DisplayName, request.Visibility, timestamp, timestamp); err != nil {
			return fmt.Errorf("insert principal: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO grants (
				id, description, principal_id, effect, server_id, upstream_name,
				constraint_json, expires_at, created_at
			) VALUES (?, ?, ?, 'allow', ?, NULL, NULL, NULL, ?)`,
			grantID, contract.DefaultGrantName, principalID, contract.SyntheticServerID, timestamp); err != nil {
			return fmt.Errorf("insert default grant: %w", err)
		}
		if err := advanceAuthorizationRevisionTx(ctx, transaction); err != nil {
			return err
		}
		principal, err := principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		_, grant, err := repository.scanGrant(transaction.QueryRowContext(ctx, grantSelect+` WHERE id = ?`, grantID), now)
		if err != nil {
			return err
		}
		creation = contract.PrincipalCreation{Principal: principal, DefaultGrant: grant}
		if err := audit.MutationTx(ctx, transaction, now, "principal", "create", contract.AuditTarget{Type: "principal", ID: principalID}); err != nil {
			return err
		}
		return audit.MutationTx(audit.WithSystem(ctx), transaction, now, "grant", "create", contract.AuditTarget{Type: "grant", ID: grantID})
	})
	return creation, repository.mapMutationError(err)
}

func (repository *Repository) PatchPrincipal(ctx context.Context, principalID string, request PatchPrincipalRequest) (contract.Principal, error) {
	expectedRevision, err := parseExpectedRevision(request.ExpectedRevision)
	if err != nil || request.DisplayName == nil && request.State == nil && request.Visibility == nil {
		return contract.Principal{}, ErrInvalidInput
	}
	if request.DisplayName != nil && !validDisplayName(*request.DisplayName) ||
		request.State != nil && !validPrincipalState(*request.State) ||
		request.Visibility != nil && !validVisibility(*request.Visibility) {
		return contract.Principal{}, ErrInvalidInput
	}
	var updated contract.Principal
	err = repository.mutateAuthorityTx(ctx, principalID, func(transaction *sql.Tx) error {
		current, err := principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		currentRevision, parseErr := strconv.ParseInt(current.Revision, 10, 64)
		if parseErr != nil {
			return errorsInvalidState("principal revision is malformed")
		}
		if currentRevision != expectedRevision {
			return ErrStaleRevision
		}
		displayName, state, visibility := current.DisplayName, current.State, current.Visibility
		if request.DisplayName != nil {
			displayName = *request.DisplayName
		}
		if request.State != nil {
			state = *request.State
		}
		if request.Visibility != nil {
			visibility = *request.Visibility
		}
		if displayName == current.DisplayName && state == current.State && visibility == current.Visibility {
			return ErrConflict
		}
		stateChanged := state != current.State
		disabling := stateChanged && state == contract.PrincipalDisabled
		if disabling {
			_, err = transaction.ExecContext(ctx, `
				UPDATE principals
				SET display_name = ?, state = ?, visibility = ?, revision = revision + 1,
				    credential_revision = credential_revision + 1,
				    credential_id = NULL, credential_verifier = NULL,
				    credential_fingerprint = NULL, credential_created_at = NULL,
				    updated_at = ?
				WHERE id = ?`, displayName, state, visibility, formatAuthorizationTime(repository.clock.Now()), principalID)
		} else {
			_, err = transaction.ExecContext(ctx, `
				UPDATE principals
				SET display_name = ?, state = ?, visibility = ?, revision = revision + 1, updated_at = ?
				WHERE id = ?`, displayName, state, visibility, formatAuthorizationTime(repository.clock.Now()), principalID)
		}
		if err != nil {
			return fmt.Errorf("update principal: %w", err)
		}
		if stateChanged {
			if err := advanceAuthorizationRevisionTx(ctx, transaction); err != nil {
				return err
			}
		}
		updated, err = principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		if disabling && current.Credential != nil {
			if err := audit.MutationTx(audit.WithSystem(ctx), transaction, repository.clock.Now(), "agent_credential", "invalidate", contract.AuditTarget{Type: "agent_credential", ID: current.Credential.ID}); err != nil {
				return err
			}
		}
		return audit.MutationTx(ctx, transaction, repository.clock.Now(), "principal", "update", contract.AuditTarget{Type: "principal", ID: principalID})
	})
	return updated, repository.mapMutationError(err)
}

func checkPrincipalCreationCapacity(ctx context.Context, transaction *sql.Tx) error {
	var principals, grants int64
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM principals`).Scan(&principals); err != nil {
		return fmt.Errorf("count principals: %w", err)
	}
	if principals >= mustLimit("principals") {
		return ErrResourceLimit
	}
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants`).Scan(&grants); err != nil {
		return fmt.Errorf("count grants: %w", err)
	}
	if grants >= mustLimit("grants") {
		return ErrResourceLimit
	}
	return nil
}

func advanceAuthorizationRevisionTx(ctx context.Context, transaction *sql.Tx) error {
	result, err := transaction.ExecContext(ctx, `UPDATE authorization_meta SET revision = revision + 1 WHERE singleton = 1`)
	if err != nil {
		return fmt.Errorf("advance authorization revision: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect authorization revision update: %w", err)
	}
	if updated != 1 {
		return errorsInvalidState("authorization metadata singleton is missing")
	}
	return nil
}

func (repository *Repository) mapMutationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrStorageLatched) {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if errors.Is(err, storage.ErrMutationBusy) {
		return fmt.Errorf("%w: %w", ErrResourceLimit, err)
	}
	if isRepositoryError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}

func parseExpectedRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != value {
		return 0, ErrInvalidInput
	}
	return revision, nil
}

func validPrincipalState(state contract.PrincipalState) bool {
	return state == contract.PrincipalActive || state == contract.PrincipalDisabled
}

func validVisibility(visibility contract.PrincipalVisibility) bool {
	return visibility == contract.VisibilityRequestable || visibility == contract.VisibilityAllowedOnly || visibility == contract.VisibilityAll
}

func formatAuthorizationTime(value time.Time) string {
	return value.UTC().Format(canonicalTimestampLayout)
}
