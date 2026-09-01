package grantrequests

import (
	"context"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type AdminService struct {
	repository *Repository
	authority  ApprovalAuthority
}

func NewAdminService(repository *Repository, authority ApprovalAuthority) (*AdminService, error) {
	if repository == nil || authority == nil {
		return nil, ErrInvalidInput
	}
	return &AdminService{repository: repository, authority: authority}, nil
}

func (service *AdminService) ListAdmin(ctx context.Context, filter AdminFilter, cursor *AdminCursor, limit int) (AdminPage, error) {
	return service.repository.ListAdmin(ctx, filter, cursor, limit)
}

func (service *AdminService) GetAdmin(ctx context.Context, requestID string) (contract.GrantRequest, error) {
	return service.repository.GetAdmin(ctx, requestID)
}

func (service *AdminService) ApproveAdmin(ctx context.Context, requestID, revision string, description *string, policy contract.Policy) (contract.GrantRequest, error) {
	before, err := service.repository.GetAdmin(ctx, requestID)
	if err != nil {
		return contract.GrantRequest{}, err
	}
	approved, err := service.repository.Approve(ctx, service.authority, ApproveRequest{
		Description: description, ID: requestID, ExpectedRevision: revision, ApprovedPolicy: policy,
	})
	if err != nil {
		return contract.GrantRequest{}, err
	}
	result := before
	result.GrantRequestSummary = requestSummary(before.PrincipalID, approved.Request)
	result.ApprovedEvidence = approved.ApprovedEvidence
	result.CurrentTarget = service.approvedComparison(ctx, before, policy, approved.ApprovedEvidence)
	return result, nil
}

func (service *AdminService) RejectAdmin(ctx context.Context, requestID, revision string, reason contract.GrantRequestRejectionReason) (contract.GrantRequest, error) {
	before, err := service.repository.GetAdmin(ctx, requestID)
	if err != nil {
		return contract.GrantRequest{}, err
	}
	rejected, err := service.repository.Reject(ctx, RejectRequest{ID: requestID, ExpectedRevision: revision, Reason: reason})
	if err != nil {
		return contract.GrantRequest{}, err
	}
	result := before
	result.GrantRequestSummary = requestSummary(before.PrincipalID, rejected)
	result.ApprovedEvidence = nil
	return result, nil
}

func (service *AdminService) approvedComparison(ctx context.Context, before contract.GrantRequest, policy contract.Policy, evidence *contract.DescriptorEvidence) contract.TargetComparison {
	if policy.Scope == contract.PolicyServer {
		return contract.TargetComparison{Scope: contract.PolicyServer, TargetState: contract.TargetExtant}
	}
	if before.RequestedPolicy.Scope == contract.PolicyTool {
		result := before.CurrentTarget
		result.Scope = contract.PolicyTool
		return result
	}
	active := contract.TargetActiveUnavailable
	durable := contract.TargetDurableAbsent
	result := contract.TargetComparison{
		Scope: contract.PolicyTool, TargetState: contract.TargetExtant,
		ActiveState: &active, DurableState: &durable,
	}
	if evidence == nil {
		return result
	}
	if evidence.DurableState == contract.EvidenceCurrent {
		durable = contract.TargetDurableCurrent
	} else {
		durable = contract.TargetDurableRetired
	}
	result.DurableState = &durable
	revision, fingerprint, descriptor := evidence.CatalogRevision, evidence.Fingerprint, evidence.Descriptor
	result.CatalogRevision, result.Fingerprint, result.Descriptor = &revision, &fingerprint, &descriptor
	if service.repository.active != nil {
		active = service.repository.active.CompareActiveTarget(ctx, evidence.ServerID, evidence.UpstreamName, evidence.Fingerprint)
		if _, err := contract.ParseTargetActiveState(string(active)); err != nil {
			active = contract.TargetActiveUnavailable
		}
		result.ActiveState = &active
	}
	return result
}
