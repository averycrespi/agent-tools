package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type CallRequest struct {
	Params    strictjson.Value
	WireValid bool
}

type CallResponse struct {
	Result             *ProjectedCallResult
	ErrorCode          contract.AgentCallErrorCode
	RejectionReason    contract.CallRejectionReason
	BlockedSelfService bool
	InvocationID       string
}

type executionLease interface {
	Execute(context.Context, json.RawMessage) downstream.CallResult
}

type LocalHandler func(context.Context, authorization.AdmittedSubject, strictjson.Value) LocalCallResult

type LocalTarget struct {
	readOnlyHint bool
	evidence     RouteEvidence
	validate     func(strictjson.Value) error
	handle       LocalHandler
}

type LocalResolver func(string) (LocalTarget, bool)

func (target LocalTarget) Validate(arguments strictjson.Value) error {
	if target.validate == nil {
		return errors.New("local invocation target is invalid")
	}
	return target.validate(arguments)
}

func NewLocalTarget(target catalog.SyntheticCallTarget, handler LocalHandler) (LocalTarget, error) {
	return NewLocalTargetWithValidation(target, nil, handler)
}

func NewLocalTargetWithValidation(target catalog.SyntheticCallTarget, additional func(strictjson.Value) error, handler LocalHandler) (LocalTarget, error) {
	descriptor := target.Descriptor
	evidence := RouteEvidence{
		ServerID: descriptor.ServerID, ToolID: descriptor.ID, UpstreamName: descriptor.UpstreamName,
		DescriptorRevision: descriptor.CatalogRevision, DescriptorFingerprint: descriptor.Fingerprint,
	}
	if descriptor.ServerID != contract.SyntheticServerID || target.Validator == nil || handler == nil || !validRouteEvidence(&evidence) {
		return LocalTarget{}, errors.New("local invocation target is invalid")
	}
	validate := target.Validator.Validate
	if additional != nil {
		validate = func(arguments strictjson.Value) error {
			if err := target.Validator.Validate(arguments); err != nil {
				return err
			}
			return additional(arguments)
		}
	}
	return LocalTarget{evidence: evidence, validate: validate, handle: handler, readOnlyHint: descriptor.Descriptor.Annotations.ReadOnlyHint}, nil
}

type callTarget struct {
	readOnlyHint bool
	evidence     RouteEvidence
	validate     func(strictjson.Value) error
	acquire      func(context.Context) (executionLease, error)
	local        LocalHandler
}

type resolveCall func(string) (callTarget, bool)

type Service struct {
	diagnostics   diagnostics.InvocationObserver
	diagnosticIDs atomic.Uint64
	audits        *Repository
	admissions    *AdmissionCoordinator
	resolve       resolveCall
}

func NewService(audits *Repository, authority *authorization.Repository, routes *catalog.RouteRegistry) (*Service, error) {
	if routes == nil {
		return nil, errors.New("invocation service dependencies are incomplete")
	}
	return newService(audits, authority, downstreamResolver(routes))
}

func NewServiceWithLocal(audits *Repository, authority *authorization.Repository, routes *catalog.RouteRegistry, local LocalResolver) (*Service, error) {
	if routes == nil || local == nil {
		return nil, errors.New("invocation service dependencies are incomplete")
	}
	downstreamResolve := downstreamResolver(routes)
	return newService(audits, authority, func(name string) (callTarget, bool) {
		downstreamTarget, downstreamFound := downstreamResolve(name)
		localTarget, localFound := local(name)
		if downstreamFound == localFound {
			return callTarget{}, false
		}
		if downstreamFound {
			return downstreamTarget, true
		}
		return callTarget{evidence: localTarget.evidence, validate: localTarget.validate, local: localTarget.handle, readOnlyHint: localTarget.readOnlyHint}, true
	})
}

func downstreamResolver(routes *catalog.RouteRegistry) resolveCall {
	return func(name string) (callTarget, bool) {
		resolved, ok := routes.ResolveCall(name)
		if !ok || resolved.Validator == nil || resolved.Capability == nil {
			return callTarget{}, false
		}
		return callTarget{
			readOnlyHint: resolved.ReadOnlyHint,
			evidence: RouteEvidence{
				ServerID: resolved.ServerID, ToolID: resolved.ToolID, UpstreamName: resolved.UpstreamName,
				DescriptorRevision: resolved.DescriptorRevision, DescriptorFingerprint: resolved.DescriptorFingerprint,
			},
			validate: resolved.Validator.Validate,
			acquire: func(ctx context.Context) (executionLease, error) {
				return resolved.Capability.Acquire(ctx)
			},
		}, true
	}
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

// SetDiagnostics binds the startup-owned observer before concurrent calls.
func (service *Service) SetDiagnostics(observer diagnostics.InvocationObserver) {
	service.diagnostics = observer
}

func (service *Service) DiagnosticCall(ctx context.Context) context.Context {
	if service.diagnostics == nil {
		return ctx
	}
	return diagnostics.WithCall(ctx, diagnostics.NextID(&service.diagnosticIDs))
}
func (service *Service) DiagnosticStopped(ctx context.Context) {
	service.callEvent(ctx, diagnostics.InvocationAdmission, diagnostics.Stopped, "", 0)
}

func (service *Service) Call(ctx context.Context, lease *authorization.Lease, request CallRequest) (response CallResponse) {
	var started time.Time
	admissionObserved := false
	if diagnostics.FromContext(ctx).Writer == diagnostics.Foreign {
		ctx = service.DiagnosticCall(ctx)
	}
	if service.diagnostics != nil && service.diagnostics.DebugEnabled() {
		started = time.Now()
		defer func() {
			if !admissionObserved {
				service.callEvent(ctx, diagnostics.InvocationAdmission, diagnostics.Unavailable, "", time.Since(started))
			}
		}()
	}
	classified := classifyCallParameters(request.Params, request.WireValid)
	admissionRequest := AuditAdmissionRequest{
		Class:             contract.AdmissionInvalidParams,
		RequestedName:     classified.name,
		RedactedArguments: redactAvailableArguments(classified.arguments),
	}
	var target callTarget
	if classified.valid {
		resolved, found := service.resolve(*classified.name)
		if !found || !validCallTarget(resolved) {
			admissionRequest.Class = contract.AdmissionUnknownTool
		} else {
			target = resolved
			route := resolved.evidence
			admissionRequest.Route = &route
			if err := resolved.validate(*classified.arguments); err != nil {
				admissionRequest.Class = contract.AdmissionInvalidArguments
			} else {
				admissionRequest.Class = contract.AdmissionEvaluated
				admissionRequest.Arguments = *classified.arguments
				admissionRequest.ReadOnlyHint = resolved.readOnlyHint
			}
		}
	}
	if classified.arguments != nil && admissionRequest.RedactedArguments == nil {
		return CallResponse{ErrorCode: contract.AuditUnavailable}
	}
	identity, err := service.audits.PrepareIdentity()
	if err != nil {
		return CallResponse{ErrorCode: contract.AuditUnavailable}
	}
	admission, err := service.admissions.Admit(ctx, lease, identity, admissionRequest)
	code, reason, mayRun := ClassifyAdmission(admission.Committed, admission.Class, admission.Decision)
	if !started.IsZero() {
		cause := diagnostics.Rejected
		acknowledgedID := ""
		if admission.Committed {
			acknowledgedID = identity.InvocationID
		}
		if admission.Committed && err == nil && mayRun && admission.DispatchAuthorized && admission.Subject != nil {
			cause = diagnostics.Success
		}
		service.callEvent(ctx, diagnostics.InvocationAdmission, cause, acknowledgedID, time.Since(started))
		admissionObserved = true
	}
	if !admission.Committed {
		return CallResponse{ErrorCode: code}
	}
	if err != nil || !mayRun || !admission.DispatchAuthorized || admission.Subject == nil {
		if mayRun {
			// An acknowledged ALLOW can still lose detachment to drain.
			code, reason = contract.CallRejected, contract.RejectionAuthorizationUnavailable
		}
		return CallResponse{
			ErrorCode: code, RejectionReason: reason, InvocationID: identity.InvocationID,
			BlockedSelfService: reason == contract.RejectionBlock && target.local != nil,
		}
	}
	if !started.IsZero() {
		ctx = context.WithValue(ctx, executionDiagnosticKey{}, time.Now())
	}
	service.callEvent(ctx, diagnostics.ExecutionStart, diagnostics.None, identity.InvocationID, 0)
	if target.local != nil {
		return service.finish(ctx, identity.InvocationID, SanitizeLocalCallResult(target.local(ctx, *admission.Subject, *classified.arguments)))
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

type executionDiagnosticKey struct{}

func (service *Service) finish(ctx context.Context, invocationID string, outcome CallOutcome) CallResponse {
	cause := diagnostics.Rejected
	if outcome.TerminalClass == contract.TerminalSucceeded {
		cause = diagnostics.Success
	}
	if outcome.TerminalClass == contract.TerminalOutcomeUnknown {
		cause = diagnostics.UnknownOutcome
	}
	var duration time.Duration
	if started, ok := ctx.Value(executionDiagnosticKey{}).(time.Time); ok {
		duration = time.Since(started)
	}
	service.callEvent(ctx, diagnostics.ExecutionResult, cause, invocationID, duration)
	if outcome.TerminalClass != "" {
		var started time.Time
		if service.diagnostics != nil && service.diagnostics.DebugEnabled() {
			started = time.Now()
		}
		terminalContext := ctx
		if service.diagnostics != nil {
			terminalContext = diagnostics.WithTerminal(ctx)
		}
		err := service.audits.AnnotateTerminal(terminalContext, invocationID, outcome.TerminalClass)
		cause := diagnostics.Success
		if err != nil {
			cause = diagnostics.Unavailable
		}
		duration = 0
		if !started.IsZero() {
			duration = time.Since(started)
		}
		service.callEvent(ctx, diagnostics.TerminalAnnotation, cause, invocationID, duration)
	}
	if outcome.Result != nil {
		return CallResponse{Result: outcome.Result}
	}
	return CallResponse{ErrorCode: outcome.ErrorCode, InvocationID: invocationID}
}

func (service *Service) callEvent(ctx context.Context, event diagnostics.Event, cause diagnostics.Cause, id string, duration time.Duration) {
	if service.diagnostics == nil || !service.diagnostics.DebugEnabled() {
		return
	}
	service.diagnostics.Invocation(diagnostics.Facts{Event: event, Cause: cause, Call: diagnostics.FromContext(ctx).Call, InvocationID: id, Duration: duration})
}

type classifiedCallParameters struct {
	name      *string
	arguments *strictjson.Value
	valid     bool
}

func classifyCallParameters(params strictjson.Value, wireValid bool) classifiedCallParameters {
	if params.Type != strictjson.ValueObject {
		return classifiedCallParameters{}
	}
	result := classifiedCallParameters{valid: wireValid}
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
	return validRouteEvidence(&target.evidence) && target.validate != nil && (target.acquire != nil) != (target.local != nil)
}
