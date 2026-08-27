package grantrequests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

// ApprovalAuthority owns the authority half of one grant request approval.
type ApprovalAuthority interface {
	ApproveGrantRequest(context.Context, authorization.GrantRequestApproval) (authorization.GrantRequestApprovalResult, error)
}

// ApproveRequest identifies one exact pending revision and its mechanically narrowed policy.
type ApproveRequest struct {
	ID               string
	ExpectedRevision string
	ApprovedPolicy   contract.Policy
}

// ApproveResult is the atomically created ordinary grant and terminal request.
type ApproveResult struct {
	Grant   contract.Grant
	Request contract.AgentGrantRequest
}

type approvalTransition struct {
	repository *Repository
	request    ApproveRequest
	approved   CompiledPolicy

	prepared           bool
	principalID        string
	serverID           string
	createdAt          string
	approvedDescriptor *catalog.DurableDescriptor
}

// Approve initiates one authorization-owned atomic approval transaction.
func (repository *Repository) Approve(ctx context.Context, authority ApprovalAuthority, request ApproveRequest) (ApproveResult, error) {
	if authority == nil || !opaqueIDPattern.MatchString(request.ID) || !validExpectedRevision(request.ExpectedRevision) {
		return ApproveResult{}, ErrInvalidInput
	}
	approved, err := CompilePolicy(request.ApprovedPolicy)
	if err != nil {
		return ApproveResult{}, ErrInvalidInput
	}
	if _, err := normalizePolicyTarget(approved); err != nil {
		return ApproveResult{}, ErrInvalidInput
	}
	transition := &approvalTransition{repository: repository, request: request, approved: approved}
	result, err := authority.ApproveGrantRequest(ctx, transition)
	if err != nil {
		return ApproveResult{}, mapApprovalError(err)
	}
	repository.invalidate(contract.Invalidation{Kind: contract.InvalidationGrantRequests})
	repository.invalidate(contract.Invalidation{Kind: contract.InvalidationAuthorization})
	return ApproveResult{Grant: result.Grant, Request: result.Request}, nil
}

func (transition *approvalTransition) PrepareGrantRequestApproval(ctx context.Context, transaction *sql.Tx) (authorization.ApprovalGrantMaterial, error) {
	if transaction == nil || transition.prepared {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	_, request, err := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, transition.request.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrNotFound
	}
	if err != nil {
		return authorization.ApprovalGrantMaterial{}, approvalReadError(err)
	}
	if request.Revision != transition.request.ExpectedRevision {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrStaleRevision
	}
	if request.State != contract.RequestPending {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrConflict
	}

	var upstream sql.NullString
	if err := transaction.QueryRowContext(ctx, `SELECT principal_id, resolved_server_id, resolved_upstream_name
		FROM grant_requests WHERE id = ?`, transition.request.ID).Scan(&transition.principalID, &transition.serverID, &upstream); err != nil {
		return authorization.ApprovalGrantMaterial{}, approvalReadError(err)
	}
	if !opaqueIDPattern.MatchString(transition.principalID) || !opaqueIDPattern.MatchString(transition.serverID) ||
		transition.serverID == contract.SyntheticServerID {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	requested, err := CompilePolicy(request.RequestedPolicy)
	if err != nil {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	requestedName, err := normalizePolicyTarget(requested)
	if err != nil {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	var submittedUpstream *string
	if upstream.Valid {
		value := upstream.String
		submittedUpstream = &value
	}
	if !equalOptionalString(requestedName.upstreamName, submittedUpstream) {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	approvedName, err := normalizePolicyTarget(transition.approved)
	if err != nil {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidInput
	}
	namespace, err := transition.repository.namespaces.LookupNamespaceTargetTx(ctx, transaction, approvedName.namespace)
	if errors.Is(err, servers.ErrNotFound) {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidInput
	}
	if err != nil {
		return authorization.ApprovalGrantMaterial{}, fmt.Errorf("%w: inspect approval namespace: %w", authorization.ErrApprovalUnavailable, err)
	}
	if !opaqueIDPattern.MatchString(namespace.ID) || namespace.Namespace != approvedName.namespace {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	if namespace.ID == contract.SyntheticServerID {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidInput
	}
	if _, err := contract.ParseDesiredServerState(string(namespace.State)); err != nil {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidState
	}
	approvedTarget := ResolvedTarget{ServerID: namespace.ID, UpstreamName: approvedName.upstreamName}
	if err := ValidateNarrowing(requested, ResolvedTarget{ServerID: transition.serverID, UpstreamName: submittedUpstream}, transition.approved, approvedTarget); err != nil {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrInvalidInput
	}
	if namespace.State == contract.DesiredServerDeleted {
		return authorization.ApprovalGrantMaterial{}, authorization.ErrConflict
	}
	if requested.Scope() == contract.PolicyServer && transition.approved.Scope() == contract.PolicyTool {
		descriptor, lookupErr := transition.repository.descriptors.LookupDurableDescriptorTx(ctx, transaction, transition.serverID, approvedName.externalName)
		if errors.Is(lookupErr, servers.ErrNotFound) {
			return authorization.ApprovalGrantMaterial{}, authorization.ErrConflict
		}
		if lookupErr != nil {
			return authorization.ApprovalGrantMaterial{}, fmt.Errorf("%w: inspect approval descriptor: %w", authorization.ErrApprovalUnavailable, lookupErr)
		}
		if descriptor.Resource.ServerID != transition.serverID || descriptor.Resource.ExternalName != approvedName.externalName ||
			descriptor.Resource.UpstreamName != *approvedName.upstreamName {
			return authorization.ApprovalGrantMaterial{}, authorization.ErrConflict
		}
		transition.approvedDescriptor = &descriptor
	}

	transition.prepared = true
	transition.createdAt = request.CreatedAt
	var duration *int64
	if seconds, present := transition.approved.DurationSeconds(); present {
		value := seconds
		duration = &value
	}
	return authorization.ApprovalGrantMaterial{
		PrincipalID: transition.principalID, ServerID: transition.serverID,
		UpstreamName: approvedTarget.UpstreamName, Constraint: transition.approved.ConstraintJSON(), DurationSeconds: duration,
	}, nil
}

func (transition *approvalTransition) CommitGrantRequestApproval(
	ctx context.Context,
	transaction *sql.Tx,
	grantID string,
	approvedAt time.Time,
) (contract.AgentGrantRequest, error) {
	if transaction == nil || !transition.prepared || !opaqueIDPattern.MatchString(grantID) {
		return contract.AgentGrantRequest{}, authorization.ErrInvalidState
	}
	timestamp, valid := canonicalRequestTime(approvedAt)
	createdAt, createdValid := canonicalStoredTime(transition.createdAt)
	if !valid || !createdValid || approvedAt.UTC().Before(createdAt) {
		return contract.AgentGrantRequest{}, authorization.ErrIdentityUnavailable
	}
	var evidence []byte
	if transition.approvedDescriptor != nil {
		approvedName, err := normalizePolicyTarget(transition.approved)
		if err != nil {
			return contract.AgentGrantRequest{}, authorization.ErrInvalidState
		}
		_, evidence, err = BuildDescriptorEvidence(*transition.approvedDescriptor, approvedName.namespace, approvedAt)
		if err != nil {
			return contract.AgentGrantRequest{}, authorization.ErrConflict
		}
		var total int64
		if err := transaction.QueryRowContext(ctx, `SELECT total_bytes FROM grant_request_evidence_bytes WHERE singleton = 1`).Scan(&total); err != nil {
			return contract.AgentGrantRequest{}, approvalReadError(err)
		}
		if total > fixedLimit("grant_request_evidence_bytes")-int64(len(evidence)) {
			return contract.AgentGrantRequest{}, authorization.ErrResourceLimit
		}
	}
	approvedPolicy := transition.approved.Contract()
	var constraint, duration any
	if approvedPolicy.Constraint != nil {
		constraint = string(*approvedPolicy.Constraint)
	}
	if approvedPolicy.DurationSeconds != nil {
		duration = *approvedPolicy.DurationSeconds
	}
	updated, err := transaction.ExecContext(ctx, `UPDATE grant_requests SET
		state = 'approved', revision = 2,
		approved_scope = ?, approved_target = ?, approved_constraint = ?, approved_duration_seconds = ?,
		approved_future_tools_acknowledged = ?, approved_grant_id = ?, approved_evidence = ?,
		updated_at = ?, closed_at = ?
		WHERE id = ? AND state = 'pending' AND revision = 1`,
		approvedPolicy.Scope, approvedPolicy.Target, constraint, duration, approvedPolicy.FutureToolsAcknowledged,
		grantID, nullableEvidence(evidence), timestamp, timestamp, transition.request.ID)
	if err != nil {
		return contract.AgentGrantRequest{}, fmt.Errorf("approve grant request: %w", err)
	}
	rows, err := updated.RowsAffected()
	if err != nil {
		return contract.AgentGrantRequest{}, fmt.Errorf("inspect grant request approval: %w", err)
	}
	if rows != 1 {
		return contract.AgentGrantRequest{}, authorization.ErrConflict
	}
	_, result, err := scanAgentRequest(transaction.QueryRowContext(ctx, agentRequestSelect+` WHERE id = ?`, transition.request.ID))
	if err != nil {
		return contract.AgentGrantRequest{}, approvalReadError(err)
	}
	if result.State != contract.RequestApproved || result.Revision != "2" || result.ApprovedGrantID == nil || *result.ApprovedGrantID != grantID {
		return contract.AgentGrantRequest{}, authorization.ErrInvalidState
	}
	return result, nil
}

func mapApprovalError(err error) error {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, authorization.ErrInvalidInput):
		return ErrInvalidInput
	case errors.Is(err, authorization.ErrIdentityUnavailable):
		return ErrIdentityUnavailable
	case errors.Is(err, authorization.ErrStaleRevision):
		return ErrStaleRevision
	case errors.Is(err, authorization.ErrConflict):
		return ErrConflict
	case errors.Is(err, authorization.ErrResourceLimit):
		return ErrResourceLimit
	case errors.Is(err, authorization.ErrInvalidState):
		return ErrInvalidState
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return fmt.Errorf("%w: request approval: %w", ErrStorageUnavailable, err)
	}
}

func approvalReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrInvalidState) {
		return fmt.Errorf("%w: invalid request approval state: %w", authorization.ErrInvalidState, err)
	}
	return fmt.Errorf("%w: read request approval state: %w", authorization.ErrApprovalUnavailable, err)
}
