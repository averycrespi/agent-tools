package invocation

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type CallResponse struct {
	Result       *ProjectedCallResult
	ErrorCode    contract.AgentCallErrorCode
	InvocationID string
}

type executionLease interface {
	Execute(context.Context, json.RawMessage) downstream.CallResult
}

type callTarget struct {
	evidence RouteEvidence
	validate func(strictjson.Value) error
	acquire  func(context.Context) (executionLease, error)
}

type resolveCall func(string) (callTarget, bool)

type Service struct {
	audits     *Repository
	admissions *AdmissionCoordinator
	resolve    resolveCall
}

func NewService(audits *Repository, authority *authorization.Repository, routes *catalog.RouteRegistry) (*Service, error) {
	if routes == nil {
		return nil, errors.New("invocation service dependencies are incomplete")
	}
	return newService(audits, authority, func(name string) (callTarget, bool) {
		resolved, ok := routes.ResolveCall(name)
		if !ok || resolved.Validator == nil || resolved.Capability == nil {
			return callTarget{}, false
		}
		return callTarget{
			evidence: RouteEvidence{
				ServerID: resolved.ServerID, ToolID: resolved.ToolID, UpstreamName: resolved.UpstreamName,
				DescriptorRevision: resolved.DescriptorRevision, DescriptorFingerprint: resolved.DescriptorFingerprint,
			},
			validate: resolved.Validator.Validate,
			acquire: func(ctx context.Context) (executionLease, error) {
				return resolved.Capability.Acquire(ctx)
			},
		}, true
	})
}

func newService(audits *Repository, authority *authorization.Repository, resolve resolveCall) (*Service, error) {
	if audits == nil || authority == nil || resolve == nil {
		return nil, errors.New("invocation service dependencies are incomplete")
	}
	admissions, err := NewAdmissionCoordinator(audits, authority)
	if err != nil {
		return nil, err
	}
	return &Service{audits: audits, admissions: admissions, resolve: resolve}, nil
}

func (service *Service) Call(ctx context.Context, lease *authorization.Lease, params strictjson.Value) CallResponse {
	classified := classifyCallParameters(params)
	request := AuditAdmissionRequest{
		Class:             contract.AdmissionInvalidParams,
		RequestedName:     classified.name,
		RedactedArguments: redactAvailableArguments(classified.arguments),
	}
	var target callTarget
	if classified.valid {
		resolved, found := service.resolve(*classified.name)
		if !found || !validCallTarget(resolved) {
			request.Class = contract.AdmissionUnknownTool
		} else {
			target = resolved
			route := resolved.evidence
			request.Route = &route
			if err := resolved.validate(*classified.arguments); err != nil {
				request.Class = contract.AdmissionInvalidArguments
			} else {
				request.Class = contract.AdmissionEvaluated
				request.Arguments = *classified.arguments
			}
		}
	}
	if classified.arguments != nil && request.RedactedArguments == nil {
		return CallResponse{ErrorCode: contract.AuditUnavailable}
	}
	identity, err := service.audits.PrepareIdentity()
	if err != nil {
		return CallResponse{ErrorCode: contract.AuditUnavailable}
	}
	admission, err := service.admissions.Admit(ctx, lease, identity, request)
	if !admission.Committed {
		return CallResponse{ErrorCode: contract.AuditUnavailable}
	}
	if err != nil || !admission.DispatchAuthorized {
		return CallResponse{ErrorCode: contract.CallRejected, InvocationID: identity.InvocationID}
	}
	arguments, err := strictjson.EncodeCompact(*classified.arguments)
	if err != nil {
		return service.finish(ctx, identity.InvocationID, failedOutcome(contract.ToolUnavailable, contract.TerminalPrestartFailure))
	}
	dispatch, err := target.acquire(ctx)
	if err != nil || dispatch == nil {
		if err == nil {
			err = errors.New("downstream capability returned no lease")
		}
		return service.finish(ctx, identity.InvocationID, SanitizeCallResult(downstream.CallResult{Failure: downstream.FailurePreStart, Err: err}))
	}
	return service.finish(ctx, identity.InvocationID, SanitizeCallResult(dispatch.Execute(ctx, json.RawMessage(arguments))))
}

func (service *Service) finish(ctx context.Context, invocationID string, outcome CallOutcome) CallResponse {
	_ = service.audits.AnnotateTerminal(ctx, invocationID, outcome.TerminalClass)
	if outcome.Result != nil {
		return CallResponse{Result: outcome.Result}
	}
	return CallResponse{ErrorCode: outcome.ErrorCode, InvocationID: invocationID}
}

type classifiedCallParameters struct {
	name      *string
	arguments *strictjson.Value
	valid     bool
}

func classifyCallParameters(params strictjson.Value) classifiedCallParameters {
	if params.Type != strictjson.ValueObject {
		return classifiedCallParameters{}
	}
	result := classifiedCallParameters{valid: true}
	seen := make(map[string]bool, len(params.Object))
	for _, member := range params.Object {
		if seen[member.Name] {
			result.valid = false
			continue
		}
		seen[member.Name] = true
		switch member.Name {
		case "name":
			if member.Value.Type != strictjson.ValueString || !validInvocationName(member.Value.String) {
				result.valid = false
				continue
			}
			name := member.Value.String
			result.name = &name
		case "arguments":
			if member.Value.Type != strictjson.ValueObject {
				result.valid = false
				continue
			}
			arguments := member.Value
			result.arguments = &arguments
		default:
			result.valid = false
		}
	}
	if result.name == nil {
		result.valid = false
	}
	if !seen["arguments"] {
		arguments := strictjson.Value{Type: strictjson.ValueObject}
		result.arguments = &arguments
	}
	return result
}

func redactAvailableArguments(arguments *strictjson.Value) []byte {
	if arguments == nil {
		return nil
	}
	redacted, err := RedactArguments(*arguments)
	if err != nil {
		return nil
	}
	return redacted
}

func validCallTarget(target callTarget) bool {
	return validRouteEvidence(&target.evidence) && target.validate != nil && target.acquire != nil
}
