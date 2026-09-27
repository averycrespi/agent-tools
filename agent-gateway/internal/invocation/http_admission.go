package invocation

import (
	"context"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

type HTTPAdmissionResult struct {
	Evidence           contract.HTTPTrafficAdmission
	Committed          bool
	DispatchAuthorized bool
	Material           *httpcredentials.Material
	receipt            *TrafficReceipt
}

// AdmitHTTP uses the existing authenticator, authority gate and selected traffic
// writer. It supplies no listener or forwarding implementation. Only a confirmed
// result authorizes one immediate dispatch; a readable row never substitutes.
func (c *AdmissionCoordinator) AdmitHTTP(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, input authorization.HTTPAccessInput, facts httppolicy.AddressFacts, materials *httpcredentials.Service, contexts ...authorization.HTTPAdmissionContext) (result HTTPAdmissionResult, err error) {
	if c == nil || c.audits.traffic == nil || !validAdmissionIdentity(identity) {
		return result, ErrInvalidInput
	}
	traffic := c.audits.traffic
	if !traffic.admissionGate.TryRLock() {
		return result, ErrTrafficCapacity
	}
	defer traffic.admissionGate.RUnlock()
	defer c.audits.publishTrafficStatus()
	evaluation, err := c.authority.EvaluateHTTPAdmission(ctx, lease, identity.InvocationID, identity.AdmittedAt, input, facts, contexts...)
	if err != nil {
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
	receipt, err := traffic.AdmitHTTP(ctx, evaluation.Evidence)
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
	err = c.authority.ConfirmHTTP(ctx, evaluation.Candidate, identity.InvocationID, materials, func(expected string, detach func() bool) bool {
		return receipt.httpAdmission == expected && traffic.confirmCandidate(ctx, receipt, identity.InvocationID, detach)
	})
	if err != nil {
		traffic.Release(receipt)
		return result, authorization.ErrAdmissionUnavailable
	}
	result.DispatchAuthorized = true
	result.receipt = receipt
	return result, nil
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
	return c.audits.traffic.CompleteHTTP(ctx, result.receipt, completion)
}
