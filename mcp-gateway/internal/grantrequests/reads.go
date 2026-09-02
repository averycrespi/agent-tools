package grantrequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

const agentRequestSelect = `SELECT
	insertion_sequence, id, state, revision,
	requested_scope, requested_target, requested_constraint, requested_duration_seconds,
	requested_future_tools_acknowledged,
	approved_scope, approved_target, approved_constraint, approved_duration_seconds,
	approved_future_tools_acknowledged, approved_grant_id, rejection_reason,
	created_at, updated_at, closed_at
FROM grant_requests`

// SelfCursor is an owner-scoped request insertion watermark and position.
type SelfCursor struct {
	Upper   int64
	After   int64
	AfterID string
}

// SelfPage is one owner-scoped request page.
type SelfPage struct {
	Items []contract.AgentGrantRequest
	Next  *SelfCursor
}

type requestScanner interface {
	Scan(...any) error
}

// GetOwned returns a request only when the stable principal owns it.
func (repository *Repository) GetOwned(ctx context.Context, principalID, requestID string) (contract.AgentGrantRequest, bool, error) {
	if !opaqueIDPattern.MatchString(principalID) || !opaqueIDPattern.MatchString(requestID) {
		return contract.AgentGrantRequest{}, false, ErrInvalidInput
	}
	var request contract.AgentGrantRequest
	found := false
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		_, loaded, scanErr := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE principal_id = ? AND id = ?`, principalID, requestID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil
		}
		if scanErr != nil {
			return scanErr
		}
		request, found = loaded, true
		return nil
	})
	return request, found, err
}

// ListOwned returns one insertion-watermarked page for a stable principal.
func (repository *Repository) ListOwned(
	ctx context.Context,
	principalID string,
	state *contract.GrantRequestState,
	cursor *SelfCursor,
	limit int,
) (SelfPage, error) {
	if !opaqueIDPattern.MatchString(principalID) || limit < 1 || int64(limit) > fixedLimit("agent_self_service_list_page") {
		return SelfPage{}, ErrInvalidInput
	}
	if state != nil {
		if _, err := contract.ParseGrantRequestState(string(*state)); err != nil {
			return SelfPage{}, ErrInvalidInput
		}
	}
	var page SelfPage
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		position := SelfCursor{}
		if cursor == nil {
			if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(max(insertion_sequence), 0) FROM grant_requests WHERE principal_id = ?`, principalID).Scan(&position.Upper); err != nil {
				return fmt.Errorf("capture owned request insertion watermark: %w", err)
			}
		} else {
			position = *cursor
			if !validSelfCursor(position) {
				return ErrStaleCursor
			}
		}
		var stateValue any
		if state != nil {
			stateValue = string(*state)
		}
		rows, err := transaction.QueryContext(ctx, agentRequestSelect+`
			WHERE principal_id = ? AND (? IS NULL OR state = ?)
			  AND (insertion_sequence > ? OR (insertion_sequence = ? AND id > ?))
			  AND insertion_sequence <= ?
			ORDER BY insertion_sequence, id LIMIT ?`,
			principalID, stateValue, stateValue,
			position.After, position.After, position.AfterID, position.Upper, limit+1)
		if err != nil {
			return fmt.Errorf("list owned grant requests: %w", err)
		}
		defer func() { _ = rows.Close() }()
		items := make([]contract.AgentGrantRequest, 0, limit+1)
		sequences := make([]int64, 0, limit+1)
		for rows.Next() {
			sequence, request, scanErr := scanAgentRequest(rows)
			if scanErr != nil {
				return scanErr
			}
			sequences = append(sequences, sequence)
			items = append(items, request)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate owned grant requests: %w", err)
		}
		if len(items) > limit {
			next := position
			next.After, next.AfterID = sequences[limit-1], items[limit-1].ID
			page.Next = &next
			items = items[:limit]
		}
		page.Items = items
		return nil
	})
	if err != nil {
		return SelfPage{}, err
	}
	return page, nil
}

func (repository *Repository) view(ctx context.Context, callback func(*sql.Tx) error) error {
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if repository.store.Latched() {
			return ErrStorageUnavailable
		}
		return callback(transaction)
	})
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	if err == nil || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrStaleCursor) ||
		errors.Is(err, ErrStorageUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, storage.ErrStorageLatched) {
		return ErrStorageUnavailable
	}
	return fmt.Errorf("%w: read grant requests: %w", ErrStorageUnavailable, err)
}

func scanAgentRequest(scanner requestScanner) (int64, contract.AgentGrantRequest, error) {
	var (
		sequence                                   int64
		request                                    contract.AgentGrantRequest
		revision                                   int64
		requestedScope, requestedTarget            string
		requestedConstraint, requestedDuration     sql.NullString
		requestedAcknowledged                      bool
		approvedScope, approvedTarget              sql.NullString
		approvedConstraint, approvedDuration       sql.NullString
		approvedAcknowledged                       sql.NullBool
		approvedGrantID, rejectionReason, closedAt sql.NullString
		createdAt, updatedAt                       string
	)
	if err := scanner.Scan(
		&sequence, &request.ID, &request.State, &revision,
		&requestedScope, &requestedTarget, &requestedConstraint, &requestedDuration, &requestedAcknowledged,
		&approvedScope, &approvedTarget, &approvedConstraint, &approvedDuration,
		&approvedAcknowledged, &approvedGrantID, &rejectionReason,
		&createdAt, &updatedAt, &closedAt,
	); err != nil {
		return 0, contract.AgentGrantRequest{}, err
	}
	if sequence < 1 || !opaqueIDPattern.MatchString(request.ID) || revision < 1 {
		return 0, contract.AgentGrantRequest{}, ErrInvalidState
	}
	if _, err := contract.ParseGrantRequestState(string(request.State)); err != nil {
		return 0, contract.AgentGrantRequest{}, ErrInvalidState
	}
	requested, requestedTargetFact, err := compileStoredPolicy(
		contract.PolicyScope(requestedScope), requestedTarget, requestedConstraint, requestedDuration, requestedAcknowledged,
	)
	if err != nil {
		return 0, contract.AgentGrantRequest{}, ErrInvalidState
	}
	created, createdValid := canonicalStoredTime(createdAt)
	updated, updatedValid := canonicalStoredTime(updatedAt)
	if !createdValid || !updatedValid || updated.Before(created) {
		return 0, contract.AgentGrantRequest{}, ErrInvalidState
	}
	request.Revision = strconv.FormatInt(revision, 10)
	request.RequestedPolicy = requested.Contract()
	request.CreatedAt, request.UpdatedAt = createdAt, updatedAt
	if closedAt.Valid {
		closed, valid := canonicalStoredTime(closedAt.String)
		if !valid || closed.Before(created) || closedAt.String != updatedAt {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		value := closedAt.String
		request.ClosedAt = &value
	}

	hasApprovedPolicy := approvedScope.Valid || approvedTarget.Valid || approvedConstraint.Valid || approvedDuration.Valid || approvedAcknowledged.Valid
	if hasApprovedPolicy {
		if !approvedScope.Valid || !approvedTarget.Valid || !approvedAcknowledged.Valid {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		approved, approvedTargetFact, compileErr := compileStoredPolicy(
			contract.PolicyScope(approvedScope.String), approvedTarget.String, approvedConstraint, approvedDuration, approvedAcknowledged.Bool,
		)
		if compileErr != nil || requestedTargetFact.namespace != approvedTargetFact.namespace {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		if narrowingErr := ValidateNarrowing(
			requested, ResolvedTarget{ServerID: contract.SyntheticServerID, UpstreamName: requestedTargetFact.upstreamName},
			approved, ResolvedTarget{ServerID: contract.SyntheticServerID, UpstreamName: approvedTargetFact.upstreamName},
		); narrowingErr != nil {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		value := approved.Contract()
		request.ApprovedPolicy = &value
	}
	if approvedGrantID.Valid {
		if !opaqueIDPattern.MatchString(approvedGrantID.String) {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		value := approvedGrantID.String
		request.ApprovedGrantID = &value
	}
	if rejectionReason.Valid {
		parsed, parseErr := contract.ParseGrantRequestRejectionReason(rejectionReason.String)
		if parseErr != nil {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
		request.RejectionReason = &parsed
	}

	switch request.State {
	case contract.RequestPending:
		if revision != 1 || createdAt != updatedAt || closedAt.Valid || hasApprovedPolicy || approvedGrantID.Valid || rejectionReason.Valid {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
	case contract.RequestApproved:
		if revision != 2 || !closedAt.Valid || !hasApprovedPolicy || !approvedGrantID.Valid || rejectionReason.Valid {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
	case contract.RequestRejected:
		if revision != 2 || !closedAt.Valid || hasApprovedPolicy || approvedGrantID.Valid || !rejectionReason.Valid {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
	case contract.RequestCancelled:
		if revision != 2 || !closedAt.Valid || hasApprovedPolicy || approvedGrantID.Valid || rejectionReason.Valid {
			return 0, contract.AgentGrantRequest{}, ErrInvalidState
		}
	default:
		return 0, contract.AgentGrantRequest{}, ErrInvalidState
	}
	return sequence, request, nil
}

func compileStoredPolicy(
	scope contract.PolicyScope,
	target string,
	constraint, duration sql.NullString,
	acknowledged bool,
) (CompiledPolicy, normalizedTarget, error) {
	policy := contract.Policy{Scope: scope, Target: target, FutureToolsAcknowledged: acknowledged}
	if constraint.Valid {
		value := jsonRaw(constraint.String)
		policy.Constraint = &value
	}
	if duration.Valid {
		value := duration.String
		policy.DurationSeconds = &value
	}
	compiled, err := CompilePolicy(policy)
	if err != nil {
		return CompiledPolicy{}, normalizedTarget{}, err
	}
	normalized, err := normalizePolicyTarget(compiled)
	if err != nil || normalized.namespace == contract.SyntheticServerNamespace {
		return CompiledPolicy{}, normalizedTarget{}, ErrInvalidPolicy
	}
	return compiled, normalized, nil
}

func canonicalStoredTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(requestTimestampLayout, value)
	return parsed, err == nil && parsed.Format(requestTimestampLayout) == value
}

func validSelfCursor(cursor SelfCursor) bool {
	return cursor.Upper > 0 && cursor.After > 0 && cursor.After <= cursor.Upper && opaqueIDPattern.MatchString(cursor.AfterID)
}
