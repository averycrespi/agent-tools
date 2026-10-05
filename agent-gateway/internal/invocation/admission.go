package invocation

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

type AuditAdmissionRequest struct {
	ReadOnlyHint                  bool
	Class                         contract.InvocationAdmissionClass
	MCP                           MCPDetails
	Arguments                     strictjson.Value
	ObservedAuthorizationRevision string
}

type AdmissionResult struct {
	observation        *TrafficObservation
	InvocationID       string
	Class              contract.InvocationAdmissionClass
	Decision           *contract.AuthorizationDecision
	Evaluated          bool
	DispatchAuthorized bool
	Subject            *authorization.AdmittedSubject
}

type AdmissionCoordinator struct {
	audits    *Repository
	authority *authorization.Repository
}

func NewAdmissionCoordinator(audits *Repository, authority *authorization.Repository) (*AdmissionCoordinator, error) {
	if audits == nil || authority == nil {
		return nil, errors.New("invocation admission dependencies are incomplete")
	}
	return &AdmissionCoordinator{audits: audits, authority: authority}, nil
}

func (c *AdmissionCoordinator) Admit(ctx context.Context, lease *authorization.Lease, identity PreparedAdmission, request AuditAdmissionRequest) (AdmissionResult, error) {
	result := AdmissionResult{InvocationID: identity.InvocationID}
	if lease == nil || !validExecutionRequest(request) {
		return result, ErrInvalidInput
	}
	var resolved *authorization.ResolvedVerification
	if request.Class == contract.AdmissionEvaluated {
		resolved = &authorization.ResolvedVerification{Target: request.MCP.Route.Target, ReadOnlyHint: request.ReadOnlyHint, Arguments: request.Arguments, ObservedAuthorizationRevision: request.ObservedAuthorizationRevision}
	}
	evaluation, err := c.authority.EvaluateAdmission(ctx, lease, identity.InvocationID, resolved)
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
	result.Evaluated, result.Class = true, evidence.Class
	// Capture validation cannot gate a valid executable request. An invalid or lost
	// identity/redaction drops both observations rather than retaining raw input.
	if c.audits.traffic != nil {
		if prepared, prepareErr := identity.WithAdmission(evidence); prepareErr == nil {
			result.observation = c.audits.traffic.ObserveMCP(prepared)
		} else {
			c.audits.traffic.drop()
		}
	}
	if evaluation.Candidate == nil {
		return result, nil
	}
	subject, err := c.authority.ConfirmEvaluation(ctx, evaluation.Candidate, identity.InvocationID)
	if err != nil {
		return result, err
	}
	result.Subject, result.DispatchAuthorized = &subject, true
	return result, nil
}

// Live route/argument validation is independent of optional redacted captures.
func validExecutionRequest(request AuditAdmissionRequest) bool {
	hasName := request.MCP.RequestedName != nil && validInvocationName(*request.MCP.RequestedName)
	switch request.Class {
	case contract.AdmissionInvalidParams:
		return request.MCP.Route == nil
	case contract.AdmissionUnknownTool:
		return hasName && request.MCP.Route == nil
	case contract.AdmissionInvalidArguments:
		return hasName && validRouteEvidence(request.MCP.Route)
	case contract.AdmissionEvaluated:
		return hasName && validRouteEvidence(request.MCP.Route) && request.Arguments.Type == strictjson.ValueObject
	default:
		return false
	}
}
