package invocation

import (
	"context"
	"database/sql"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type AuditAdmissionRequest struct {
	Class                         contract.InvocationAdmissionClass
	RequestedName                 *string
	RedactedArguments             []byte
	Route                         *RouteEvidence
	Arguments                     strictjson.Value
	ObservedAuthorizationRevision string
}

type AdmissionResult struct {
	InvocationID       string
	Class              contract.InvocationAdmissionClass
	Decision           *contract.AuthorizationDecision
	Committed          bool
	DispatchAuthorized bool
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

func (coordinator *AdmissionCoordinator) Admit(
	ctx context.Context,
	lease *authorization.Lease,
	identity PreparedAdmission,
	request AuditAdmissionRequest,
) (AdmissionResult, error) {
	result := AdmissionResult{InvocationID: identity.InvocationID}
	if lease == nil || !validAdmissionIdentity(identity) || !validAuditAdmissionRequest(request) {
		return result, ErrInvalidInput
	}
	var pending *authorization.PendingDetachment
	err := coordinator.authority.WithAdmission(ctx, lease, func(admission *authorization.Admission) error {
		mutationErr := coordinator.audits.mutate(ctx, func(transaction *sql.Tx) error {
			binding := lease.Binding()
			evidence := Admission{
				PrincipalID: binding.PrincipalID, CredentialID: binding.CredentialID,
				CredentialFingerprint: binding.CredentialFingerprint, CredentialRevision: binding.CredentialRevision,
				Class: request.Class, RequestedName: request.RequestedName,
				RedactedArguments: request.RedactedArguments, Route: request.Route,
			}
			if request.Class != contract.AdmissionEvaluated {
				if _, err := admission.VerifyBindingOnlyTx(ctx, transaction); err != nil {
					return err
				}
				prepared, err := identity.WithAdmission(evidence)
				if err != nil {
					return err
				}
				if err := coordinator.audits.InsertTx(ctx, transaction, prepared); err != nil {
					return err
				}
				result.Class = request.Class
				return nil
			}

			authorizationResult, detachment, phase, err := admission.VerifyResolvedTx(ctx, transaction, authorization.ResolvedVerification{
				ServerID: request.Route.ServerID, UpstreamName: request.Route.UpstreamName,
				Arguments: request.Arguments, ObservedAuthorizationRevision: request.ObservedAuthorizationRevision,
			})
			if err != nil {
				if phase != authorization.ResolvedBindingVerified || !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
					return err
				}
				evidence.Class = contract.AdmissionAuthorizationUnavailable
				prepared, prepareErr := identity.WithAdmission(evidence)
				if prepareErr != nil {
					return prepareErr
				}
				if insertErr := coordinator.audits.InsertTx(ctx, transaction, prepared); insertErr != nil {
					return insertErr
				}
				result.Class = contract.AdmissionAuthorizationUnavailable
				return nil
			}
			if phase != authorization.ResolvedEvaluated {
				return authorization.ErrAuthorizationUnavailable
			}
			evidence.Authorization = &AuthorizationEvidence{
				Decision: authorizationResult.Decision, AuthorizationRevision: authorizationResult.AuthorizationRevision,
				EvaluatedAt: authorizationResult.EvaluatedAt, GrantID: authorizationResult.GrantID,
			}
			prepared, err := identity.WithAdmission(evidence)
			if err != nil {
				return err
			}
			if err := coordinator.audits.InsertTx(ctx, transaction, prepared); err != nil {
				return err
			}
			decision := authorizationResult.Decision
			result.Class = contract.AdmissionEvaluated
			result.Decision = &decision
			pending = detachment
			return nil
		})
		if mutationErr != nil {
			return mutationErr
		}
		result.Committed = true
		if pending == nil {
			return nil
		}
		if err := pending.CommitSucceeded(); err != nil {
			return err
		}
		result.DispatchAuthorized = true
		return nil
	})
	return result, err
}

func validAdmissionIdentity(identity PreparedAdmission) bool {
	return identity.admission.Class == "" && validOpaqueInvocationID(identity.InvocationID) && identity.AdmittedAt != ""
}

func validAuditAdmissionRequest(request AuditAdmissionRequest) bool {
	hasCall := request.RequestedName != nil && request.RedactedArguments != nil
	if request.RequestedName != nil && !validInvocationName(*request.RequestedName) ||
		request.RedactedArguments != nil && !validRedactedArguments(request.RedactedArguments) {
		return false
	}
	switch request.Class {
	case contract.AdmissionInvalidParams:
		return request.Route == nil
	case contract.AdmissionUnknownTool:
		return hasCall && request.Route == nil
	case contract.AdmissionInvalidArguments:
		return hasCall && validRouteEvidence(request.Route)
	case contract.AdmissionEvaluated:
		return hasCall && validRouteEvidence(request.Route) && request.Arguments.Type == strictjson.ValueObject
	default:
		return false
	}
}
