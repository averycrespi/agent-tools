package grantrequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

// RejectRequest is one exact administrator rejection transition.
type RejectRequest struct {
	ID               string
	ExpectedRevision string
	Reason           contract.GrantRequestRejectionReason
}

type cancellationOutcomeError struct {
	result contract.CancelGrantRequestResult
}

func (err *cancellationOutcomeError) Error() string {
	return "grant request cancellation settled without mutation"
}

// CancelOwned cancels one pending owner request or returns a closed non-enumerating outcome.
func (repository *Repository) CancelOwned(ctx context.Context, principalID, requestID string) (contract.CancelGrantRequestResult, error) {
	if !opaqueIDPattern.MatchString(principalID) || !opaqueIDPattern.MatchString(requestID) {
		return contract.CancelGrantRequestResult{}, ErrInvalidInput
	}
	var result contract.CancelGrantRequestResult
	mutationComplete := false
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		_, request, scanErr := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE principal_id = ? AND id = ?`, principalID, requestID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return &cancellationOutcomeError{result: contract.CancelGrantRequestResult{Outcome: contract.RequestCancellationNotFound}}
		}
		if scanErr != nil {
			return scanErr
		}
		switch request.State {
		case contract.RequestCancelled:
			return cancellationResult(contract.RequestCancellationAlreadyCancelled, request)
		case contract.RequestApproved, contract.RequestRejected:
			return cancellationResult(contract.RequestCancellationNotPending, request)
		case contract.RequestPending:
		default:
			return ErrInvalidState
		}
		timestamp, err := lifecycleTimestamp(repository, request.CreatedAt)
		if err != nil {
			return err
		}
		updated, err := transaction.ExecContext(ctx, `UPDATE grant_requests SET
			state = 'cancelled', revision = 2, updated_at = ?, closed_at = ?
			WHERE id = ? AND principal_id = ? AND state = 'pending' AND revision = 1`,
			timestamp, timestamp, requestID, principalID)
		if err != nil {
			return fmt.Errorf("cancel grant request: %w", err)
		}
		if err := requireSingleTransition(updated); err != nil {
			return err
		}
		_, transitioned, err := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE principal_id = ? AND id = ?`, principalID, requestID))
		if err != nil {
			return fmt.Errorf("read cancelled grant request: %w", err)
		}
		if transitioned.State != contract.RequestCancelled || transitioned.Revision != "2" {
			return ErrInvalidState
		}
		result = contract.CancelGrantRequestResult{Outcome: contract.RequestCancellationCancelled, Request: &transitioned}
		mutationComplete = true
		return nil
	})
	if err != nil {
		if mutationComplete {
			return contract.CancelGrantRequestResult{}, ErrStorageOutcomeUncertain
		}
		var outcome *cancellationOutcomeError
		if errors.As(err, &outcome) {
			return outcome.result, nil
		}
		return contract.CancelGrantRequestResult{}, repository.lifecycleError(err)
	}
	repository.invalidate(contract.Invalidation{Kind: contract.InvalidationGrantRequests})
	return result, nil
}

// Reject performs one exact pending-to-rejected administrator transition.
func (repository *Repository) Reject(ctx context.Context, request RejectRequest) (contract.AgentGrantRequest, error) {
	if !opaqueIDPattern.MatchString(request.ID) || !validExpectedRevision(request.ExpectedRevision) {
		return contract.AgentGrantRequest{}, ErrInvalidInput
	}
	if _, err := contract.ParseGrantRequestRejectionReason(string(request.Reason)); err != nil {
		return contract.AgentGrantRequest{}, ErrInvalidInput
	}
	var result contract.AgentGrantRequest
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		_, current, scanErr := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, request.ID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return scanErr
		}
		if current.Revision != request.ExpectedRevision {
			return ErrStaleRevision
		}
		if current.State != contract.RequestPending {
			return ErrConflict
		}
		timestamp, err := lifecycleTimestamp(repository, current.CreatedAt)
		if err != nil {
			return err
		}
		updated, err := transaction.ExecContext(ctx, `UPDATE grant_requests SET
			state = 'rejected', revision = 2, rejection_reason = ?, updated_at = ?, closed_at = ?
			WHERE id = ? AND state = 'pending' AND revision = 1`, request.Reason, timestamp, timestamp, request.ID)
		if err != nil {
			return fmt.Errorf("reject grant request: %w", err)
		}
		if err := requireSingleTransition(updated); err != nil {
			return err
		}
		_, result, err = scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, request.ID))
		if err != nil {
			return fmt.Errorf("read rejected grant request: %w", err)
		}
		if result.State != contract.RequestRejected || result.Revision != "2" || result.RejectionReason == nil || *result.RejectionReason != request.Reason {
			return ErrInvalidState
		}
		return nil
	})
	if err != nil {
		return contract.AgentGrantRequest{}, repository.lifecycleError(err)
	}
	repository.invalidate(contract.Invalidation{Kind: contract.InvalidationGrantRequests})
	return result, nil
}

func cancellationResult(outcome contract.CancelGrantRequestOutcome, request contract.AgentGrantRequest) error {
	return &cancellationOutcomeError{result: contract.CancelGrantRequestResult{Outcome: outcome, Request: &request}}
}

func lifecycleTimestamp(repository *Repository, createdAt string) (string, error) {
	now := repository.clock.Now()
	timestamp, valid := canonicalRequestTime(now)
	created, createdValid := canonicalStoredTime(createdAt)
	if !valid || !createdValid || now.UTC().Before(created) {
		return "", ErrIdentityUnavailable
	}
	return timestamp, nil
}

func requireSingleTransition(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect grant request transition: %w", err)
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}

func validExpectedRevision(value string) bool {
	revision, err := strconv.ParseInt(value, 10, 64)
	return err == nil && revision > 0 && strconv.FormatInt(revision, 10) == value
}

func (repository *Repository) lifecycleError(err error) error {
	if errors.Is(err, storage.ErrStorageLatched) || errors.Is(err, storage.ErrMutationBusy) || repository.store.Latched() {
		return ErrStorageUnavailable
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) ||
		errors.Is(err, ErrInvalidState) || errors.Is(err, ErrStaleRevision) || errors.Is(err, ErrConflict) {
		return err
	}
	return fmt.Errorf("%w: grant request lifecycle: %w", ErrStorageUnavailable, err)
}
