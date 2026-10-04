package invocation

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

type HTTPAdmissionResult struct {
	Evidence           contract.HTTPTrafficAdmission
	Committed          bool
	DispatchAuthorized bool
	Material           *httpcredentials.Material
	// Closed diagnostic facts preserve the failing boundary without exposing errors.
	FailureStage diagnostics.Stage
	FailureCause diagnostics.Cause
	receipt      *TrafficReceipt
	candidate    *authorization.HTTPEvaluationCandidate
}

// AdmitHTTP uses the existing authenticator, authority gate and selected traffic
// writer. It supplies no listener or forwarding implementation. Only a confirmed
// result authorizes one immediate dispatch; a readable row never substitutes.
func (c *AdmissionCoordinator) AdmitHTTP(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, input authorization.HTTPAccessInput, facts httppolicy.AddressFacts, materials *httpcredentials.Service, contexts ...authorization.HTTPAdmissionContext) (result HTTPAdmissionResult, err error) {
	stage := diagnostics.ProxyEvaluation
	var failure error
	defer func() {
		if err != nil {
			if failure == nil {
				failure = err
			}
			result.FailureStage, result.FailureCause = stage, httpAdmissionCause(failure)
		}
	}()
	if c == nil || c.audits.traffic == nil || !validAdmissionIdentity(identity) {
		return result, ErrInvalidInput
	}
	traffic := c.audits.traffic
	if !traffic.admissionGate.TryRLock() {
		stage = diagnostics.ProxyTraffic
		return result, ErrTrafficCapacity
	}
	defer traffic.admissionGate.RUnlock()
	defer c.audits.publishTrafficStatus()
	evaluation, err := c.authority.EvaluateHTTPAdmission(ctx, lease, identity.InvocationID, identity.AdmittedAt, input, facts, contexts...)
	if err != nil {
		failure = err
		return result, authorization.ErrAdmissionUnavailable
	}
	result.Evidence = evaluation.Evidence
	var materialErr error
	if evaluation.Evidence.Material != nil {
		if materials == nil {
			materialErr = httpcredentials.ErrUnavailable
		} else {
			result.Material, materialErr = materials.Acquire(ctx, evaluation.Evidence.Material.Credential)
			if materialErr == nil && result.Material.Generation() != evaluation.Evidence.Material.Generation {
				materialErr = httpcredentials.ErrUnavailable
			}
		}
	}
	defer func() {
		if !result.DispatchAuthorized && result.Material != nil {
			result.Material.Clear()
			result.Material = nil
		}
	}()
	stage = diagnostics.ProxyTraffic
	receipt, err := traffic.AdmitHTTP(ctx, evaluation.Evidence)
	if err != nil {
		return result, err
	}
	result.Committed = true
	if evaluation.Candidate == nil || materialErr != nil {
		traffic.Release(receipt)
		if materialErr != nil {
			stage, failure = diagnostics.ProxyMaterial, materialErr
			return result, authorization.ErrAdmissionUnavailable
		}
		return result, nil
	}
	stage = diagnostics.ProxyConfirmation
	err = c.authority.ConfirmHTTP(ctx, evaluation.Candidate, identity.InvocationID, materials, func(expected string, detach func() bool) bool {
		return receipt.httpAdmission == expected && traffic.confirmCandidate(ctx, receipt, identity.InvocationID, detach)
	})
	if err != nil {
		failure = err
		traffic.Release(receipt)
		return result, authorization.ErrAdmissionUnavailable
	}
	result.DispatchAuthorized = true
	result.receipt = receipt
	result.candidate = evaluation.Candidate
	return result, nil
}

func httpAdmissionCause(err error) diagnostics.Cause {
	switch {
	case errors.Is(err, context.Canceled):
		return diagnostics.Cancelled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrTrafficDeadline), errors.Is(err, storage.ErrMutationWaitExpired):
		return diagnostics.Expired
	case errors.Is(err, ErrTrafficCapacity), errors.Is(err, authorization.ErrResourceLimit), errors.Is(err, storage.ErrMutationWaitFull):
		return diagnostics.Capacity
	case errors.Is(err, authorization.ErrShuttingDown), errors.Is(err, storage.ErrMutationWaitStopped):
		return diagnostics.Stopped
	case errors.Is(err, storage.ErrStorageLatched):
		return diagnostics.Latched
	default:
		return diagnostics.Unavailable
	}
}

// CompleteHTTP makes the sole synchronous best-effort attempt; its error is
// evidence availability only and must not replace an already known live result.
func (c *AdmissionCoordinator) CompleteHTTP(ctx context.Context, result HTTPAdmissionResult, completion contract.HTTPTrafficCompletion) error {
	if c == nil || c.audits.traffic == nil || !result.DispatchAuthorized {
		return ErrInvalidInput
	}
	if result.Material != nil {
		defer result.Material.Clear()
	}
	defer c.audits.publishTrafficStatus()
	defer c.authority.ReleaseOpaque(result.candidate)
	return c.audits.traffic.CompleteHTTP(ctx, result.receipt, completion)
}
