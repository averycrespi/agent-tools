package invocation

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func (coordinator *AdmissionCoordinator) admitTraffic(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, request AuditAdmissionRequest) (AdmissionResult, error) {
	result := AdmissionResult{InvocationID: identity.InvocationID}
	traffic := coordinator.audits.traffic
	defer coordinator.audits.publishTrafficStatus()
	if !traffic.admissionGate.TryRLock() {
		return result, ErrTrafficCapacity
	}
	defer traffic.admissionGate.RUnlock()
	if lease == nil || !validAdmissionIdentity(identity) || !validAuditAdmissionRequest(request) {
		return result, ErrInvalidInput
	}
	var resolved *authorization.ResolvedVerification
	if request.Class == contract.AdmissionEvaluated {
		resolved = &authorization.ResolvedVerification{Target: request.MCP.Route.Target, ReadOnlyHint: request.ReadOnlyHint, Arguments: request.Arguments, ObservedAuthorizationRevision: request.ObservedAuthorizationRevision}
	}
	evaluation, err := coordinator.authority.EvaluateAdmission(ctx, lease, identity.InvocationID, resolved)
	binding := lease.Binding()
	evidence := Admission{Admission: activity.Admission{PrincipalID: binding.PrincipalID, CredentialID: binding.CredentialID, CredentialFingerprint: binding.CredentialFingerprint, CredentialRevision: binding.CredentialRevision, Class: request.Class}, MCP: request.MCP}
	if err != nil {
		if evaluation.Phase != authorization.ResolvedBindingVerified || !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
			return result, err
		}
		evidence.Class = contract.AdmissionAuthorizationUnavailable
	} else if resolved != nil {
		if evaluation.Phase != authorization.ResolvedEvaluated {
			return result, authorization.ErrAuthorizationUnavailable
		}
		evidence.Authorization = &activity.Authorization{Decision: evaluation.Result.Decision, AuthorizationRevision: evaluation.Result.AuthorizationRevision, EvaluatedAt: evaluation.Result.EvaluatedAt, GrantID: evaluation.Result.GrantID}
		decision := evaluation.Result.Decision
		result.Decision = &decision
	}
	prepared, err := identity.WithAdmission(evidence)
	if err != nil {
		return result, err
	}
	receipt, err := traffic.Admit(ctx, prepared)
	if err != nil {
		return result, err
	}
	result.Committed, result.Class = true, evidence.Class
	coordinator.audits.publish(identity.InvocationID)
	if evaluation.Candidate == nil {
		traffic.Release(receipt)
		return result, nil
	}
	subject, err := coordinator.authority.ConfirmEvaluation(ctx, evaluation.Candidate, identity.InvocationID, func(expected contract.AuthorizationResult, detach func() bool) bool {
		actual := receipt.evidence.admission.Authorization
		if actual == nil || actual.Decision != expected.Decision || actual.AuthorizationRevision != expected.AuthorizationRevision || actual.EvaluatedAt != expected.EvaluatedAt || (actual.GrantID == nil) != (expected.GrantID == nil) || (actual.GrantID != nil && *actual.GrantID != *expected.GrantID) {
			return false
		}
		return traffic.confirmCandidate(ctx, receipt, identity.InvocationID, detach)
	})
	if err != nil {
		traffic.Release(receipt)
		return result, err
	}
	result.receipt, result.Subject, result.DispatchAuthorized = receipt, &subject, true
	return result, nil
}
