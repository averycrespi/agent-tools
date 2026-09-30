package invocation

import (
	"context"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type GitAdmissionResult struct {
	Evidence                      contract.GitTrafficAdmission
	Committed, DispatchAuthorized bool
	Material                      *gitcredentials.Material
	receipt                       *TrafficReceipt
}

func (c *AdmissionCoordinator) AdmitGit(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, request *gitwire.Request, facts httppolicy.AddressFacts, materials *gitcredentials.Service) (result GitAdmissionResult, err error) {
	if c == nil || c.audits.traffic == nil || !validAdmissionIdentity(identity) {
		return result, ErrInvalidInput
	}
	traffic := c.audits.traffic
	if !traffic.admissionGate.TryRLock() {
		return result, ErrTrafficCapacity
	}
	defer traffic.admissionGate.RUnlock()
	defer c.audits.publishTrafficStatus()
	evaluation, err := c.authority.EvaluateGitAdmission(ctx, lease, identity.InvocationID, identity.AdmittedAt, request, facts)
	if err != nil {
		return result, authorization.ErrAdmissionUnavailable
	}
	result.Evidence = evaluation.Evidence
	var materialErr error
	if evaluation.Evidence.Material != nil {
		if materials == nil {
			materialErr = gitcredentials.ErrUnavailable
		} else {
			result.Material, materialErr = materials.Acquire(ctx, evaluation.Evidence.Material.Credential)
			if materialErr == nil && result.Material.Generation() != evaluation.Evidence.Material.Generation {
				materialErr = gitcredentials.ErrUnavailable
			}
		}
	}
	defer func() {
		if !result.DispatchAuthorized && result.Material != nil {
			result.Material.Clear()
			result.Material = nil
		}
	}()
	receipt, err := traffic.AdmitGit(ctx, evaluation.Evidence)
	if err != nil {
		return result, err
	}
	result.Committed = true
	if evaluation.Candidate == nil || materialErr != nil {
		traffic.Release(receipt)
		if materialErr != nil {
			return result, authorization.ErrAdmissionUnavailable
		}
		return result, nil
	}
	err = c.authority.ConfirmGit(ctx, evaluation.Candidate, identity.InvocationID, request, materials, func(expected string, detach func() bool) bool {
		return receipt.gitAdmission == expected && traffic.confirmCandidate(ctx, receipt, identity.InvocationID, detach)
	})
	if err != nil {
		traffic.Release(receipt)
		return result, authorization.ErrAdmissionUnavailable
	}
	result.DispatchAuthorized = true
	result.receipt = receipt
	return result, nil
}
func (c *AdmissionCoordinator) CompleteGit(ctx context.Context, result GitAdmissionResult, completion contract.GitTrafficCompletion) error {
	if c == nil || c.audits.traffic == nil || !result.DispatchAuthorized {
		return ErrInvalidInput
	}
	if result.Material != nil {
		defer result.Material.Clear()
	}
	defer c.audits.publishTrafficStatus()
	return c.audits.traffic.CompleteGit(ctx, result.receipt, completion)
}
