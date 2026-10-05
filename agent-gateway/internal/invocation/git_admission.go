package invocation

import (
	"context"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitwire"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type GitAdmissionResult struct {
	Execution                     authorization.GitExecution
	Evaluated, DispatchAuthorized bool
	Material                      *gitcredentials.Material
	observation                   *TrafficObservation
	owner                         *gitExecutionOwner
}
type gitExecutionOwner struct {
	once     sync.Once
	material *gitcredentials.Material
}

// Settle belongs to the network owner, never the history writer.
func (result GitAdmissionResult) Settle() {
	if result.owner != nil {
		result.owner.once.Do(func() {
			if result.owner.material != nil {
				result.owner.material.Clear()
			}
		})
	}
}

func (c *AdmissionCoordinator) AdmitGit(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, request *gitwire.Request, facts httppolicy.AddressFacts, materials *gitcredentials.Service) (result GitAdmissionResult, err error) {
	if c == nil {
		return result, ErrInvalidInput
	}
	evaluation, err := c.authority.EvaluateGitAdmission(ctx, lease, identity.InvocationID, identity.AdmittedAt, request, facts)
	if err != nil {
		return result, authorization.ErrAdmissionUnavailable
	}
	result.Execution, result.Evaluated = evaluation.Execution, true
	var materialErr error
	if evaluation.Execution.Material != nil {
		if materials == nil {
			materialErr = gitcredentials.ErrUnavailable
		} else {
			result.Material, materialErr = materials.Acquire(ctx, evaluation.Execution.Material.Credential)
			if materialErr == nil && result.Material.Generation() != evaluation.Execution.Material.Generation {
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
	if materialErr != nil {
		evaluation.Evidence.Allowed = false
		evaluation.Evidence.Denial = "credential_unavailable"
		evaluation.Evidence.Material = nil
		result.Execution.Allowed = false
		result.Execution.CredentialUnavailable = true
	}
	if c.audits.traffic != nil {
		result.observation = c.audits.traffic.ObserveGit(evaluation.Evidence)
	}
	if evaluation.Candidate == nil || materialErr != nil {
		if materialErr != nil {
			return result, authorization.ErrAdmissionUnavailable
		}
		return result, nil
	}
	if err = c.authority.ConfirmGit(ctx, evaluation.Candidate, identity.InvocationID, request, materials); err != nil {
		return result, authorization.ErrAdmissionUnavailable
	}
	result.DispatchAuthorized = true
	result.owner = &gitExecutionOwner{material: result.Material}
	return result, nil
}
func (c *AdmissionCoordinator) RejectGit(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, reason string) error {
	if c == nil {
		return ErrInvalidInput
	}
	evidence, err := c.authority.GitRejection(ctx, lease, identity.InvocationID, identity.AdmittedAt, reason)
	if err != nil {
		return err
	}
	if c.audits.traffic != nil {
		c.audits.traffic.ObserveGit(evidence)
	}
	return nil
}
func (c *AdmissionCoordinator) CompleteGit(_ context.Context, result GitAdmissionResult, completion contract.GitTrafficCompletion) error {
	if c == nil || !result.DispatchAuthorized {
		return ErrInvalidInput
	}
	return c.audits.traffic.ObserveGitCompletion(result.observation, completion)
}
