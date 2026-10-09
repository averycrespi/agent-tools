package invocation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type HTTPAdmissionResult struct {
	Execution          authorization.HTTPExecution
	Evaluated          bool
	DispatchAuthorized bool
	Material           *httpcredentials.Material
	FailureStage       diagnostics.Stage
	FailureCause       diagnostics.Cause
	FailureDetail      diagnostics.Detail
	observation        *TrafficObservation
	owner              *httpExecutionOwner
}
type httpExecutionOwner struct {
	once      sync.Once
	authority *authorization.Repository
	candidate *authorization.HTTPEvaluationCandidate
	material  *httpcredentials.Material
}

// Settle releases execution-owned resources only after actual network cleanup.
// It must be called even when no terminal observation can be captured.
func (result HTTPAdmissionResult) Settle() {
	if result.owner == nil {
		return
	}
	result.owner.once.Do(func() {
		if result.owner.material != nil {
			result.owner.material.Clear()
		}
		result.owner.authority.ReleaseOpaque(result.owner.candidate)
	})
}

// AdmitHTTP confirms one request-local evaluation independently of optional history.
func (c *AdmissionCoordinator) AdmitHTTP(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, input authorization.HTTPAccessInput, facts httppolicy.AddressFacts, materials *httpcredentials.Service, contexts ...authorization.HTTPAdmissionContext) (result HTTPAdmissionResult, err error) {
	stage := diagnostics.ProxyEvaluation
	var failure error
	defer func() {
		if err != nil {
			if failure == nil {
				failure = err
			}
			result.FailureStage, result.FailureCause = stage, httpAdmissionCause(failure)
			resource := ""
			if result.Execution.Material != nil {
				resource = result.Execution.Material.Credential.ID
			}
			result.FailureDetail = diagnostics.Snapshot("http-admission", "acquire/confirm material", resource, failure)
			result.FailureDetail.Effect = "dispatch=not_authorized; fallback=not_attempted"
		}
	}()
	if c == nil {
		return result, ErrInvalidInput
	}
	evaluation, err := c.authority.EvaluateHTTPAdmission(ctx, lease, identity.InvocationID, identity.AdmittedAt, input, facts, contexts...)
	if err != nil {
		failure = err
		return result, authorization.ErrAdmissionUnavailable
	}
	result.Execution, result.Evaluated = evaluation.Execution, true
	var materialErr error
	if evaluation.Execution.Material != nil {
		if materials == nil {
			materialErr = httpcredentials.ErrUnavailable
		} else {
			result.Material, materialErr = materials.Acquire(ctx, evaluation.Execution.Material.Credential)
			if materialErr == nil && result.Material.Generation() != evaluation.Execution.Material.Generation {
				materialErr = diagnostics.WithDetail(httpcredentials.ErrUnavailable, diagnostics.Detail{Component: "http-admission", Operation: "compare generation", Explanation: fmt.Sprintf("rule=material_generation expected=%s observed=%s", evaluation.Execution.Material.Generation, result.Material.Generation())})
			}
		}
	}
	defer func() {
		if !result.DispatchAuthorized && result.Material != nil {
			result.Material.Clear()
			result.Material = nil
		}
	}()
	if c.audits.traffic != nil {
		result.observation = c.audits.traffic.ObserveHTTP(evaluation.Evidence)
	}
	if evaluation.Candidate == nil || materialErr != nil {
		if materialErr != nil {
			stage, failure = diagnostics.ProxyMaterial, materialErr
			return result, authorization.ErrAdmissionUnavailable
		}
		return result, nil
	}
	stage = diagnostics.ProxyConfirmation
	if err = c.authority.ConfirmHTTP(ctx, evaluation.Candidate, identity.InvocationID, materials); err != nil {
		failure = err
		return result, authorization.ErrAdmissionUnavailable
	}
	result.DispatchAuthorized = true
	result.owner = &httpExecutionOwner{authority: c.authority, candidate: evaluation.Candidate, material: result.Material}
	return result, nil
}

func httpAdmissionCause(err error) diagnostics.Cause {
	switch {
	case errors.Is(err, context.Canceled):
		return diagnostics.Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return diagnostics.Expired
	case errors.Is(err, authorization.ErrResourceLimit):
		return diagnostics.Capacity
	case errors.Is(err, authorization.ErrShuttingDown):
		return diagnostics.Stopped
	case errors.Is(err, storage.ErrStorageLatched):
		return diagnostics.Latched
	default:
		return diagnostics.Unavailable
	}
}

// CompleteHTTP offers sanitized terminal facts without waiting for persistence.
func (c *AdmissionCoordinator) CompleteHTTP(_ context.Context, result HTTPAdmissionResult, completion contract.HTTPTrafficCompletion) error {
	if c == nil || !result.DispatchAuthorized {
		return ErrInvalidInput
	}
	return c.audits.traffic.ObserveHTTPCompletion(result.observation, completion)
}
