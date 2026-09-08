package contract

type InvocationAdmissionClass string

const (
	AdmissionInvalidParams            InvocationAdmissionClass = "invalid_params"
	AdmissionUnknownTool              InvocationAdmissionClass = "unknown_tool"
	AdmissionInvalidArguments         InvocationAdmissionClass = "invalid_arguments"
	AdmissionAuthorizationUnavailable InvocationAdmissionClass = "authorization_unavailable"
	AdmissionEvaluated                InvocationAdmissionClass = "evaluated"
)

func InvocationAdmissionClasses() []InvocationAdmissionClass {
	return []InvocationAdmissionClass{
		AdmissionInvalidParams,
		AdmissionUnknownTool,
		AdmissionInvalidArguments,
		AdmissionAuthorizationUnavailable,
		AdmissionEvaluated,
	}
}

func ParseInvocationAdmissionClass(value string) (InvocationAdmissionClass, error) {
	return parseClosed(value, InvocationAdmissionClasses())
}

type InvocationTerminalClass string

const (
	TerminalPrestartFailure   InvocationTerminalClass = "prestart_failure"
	TerminalSucceeded         InvocationTerminalClass = "succeeded"
	TerminalDownstreamFailure InvocationTerminalClass = "downstream_failure"
	TerminalOutcomeUnknown    InvocationTerminalClass = "outcome_unknown"
)

func InvocationTerminalClasses() []InvocationTerminalClass {
	return []InvocationTerminalClass{
		TerminalPrestartFailure,
		TerminalSucceeded,
		TerminalDownstreamFailure,
		TerminalOutcomeUnknown,
	}
}

func ParseInvocationTerminalClass(value string) (InvocationTerminalClass, error) {
	return parseClosed(value, InvocationTerminalClasses())
}

type AgentCallErrorCode string

const (
	CallRejected      AgentCallErrorCode = "call_rejected"
	AuditUnavailable  AgentCallErrorCode = "audit_unavailable"
	ToolUnavailable   AgentCallErrorCode = "tool_unavailable"
	DownstreamFailure AgentCallErrorCode = "downstream_failure"
	OutcomeUnknown    AgentCallErrorCode = "outcome_unknown"
)

func AgentCallErrorCodes() []AgentCallErrorCode {
	return []AgentCallErrorCode{
		CallRejected,
		AuditUnavailable,
		ToolUnavailable,
		DownstreamFailure,
		OutcomeUnknown,
	}
}

func ParseAgentCallErrorCode(value string) (AgentCallErrorCode, error) {
	return parseClosed(value, AgentCallErrorCodes())
}

type CallRejectionReason string

const (
	RejectionInvalidParams            CallRejectionReason = "invalid_params"
	RejectionUnknownTool              CallRejectionReason = "unknown_tool"
	RejectionInvalidArguments         CallRejectionReason = "invalid_arguments"
	RejectionDeny                     CallRejectionReason = "deny"
	RejectionBlock                    CallRejectionReason = "block"
	RejectionAuthorizationUnavailable CallRejectionReason = "authorization_unavailable"
)

func CallRejectionReasons() []CallRejectionReason {
	return []CallRejectionReason{
		RejectionInvalidParams, RejectionUnknownTool, RejectionInvalidArguments,
		RejectionDeny, RejectionBlock, RejectionAuthorizationUnavailable,
	}
}

func ParseCallRejectionReason(value string) (CallRejectionReason, error) {
	return parseClosed(value, CallRejectionReasons())
}

func CallRejectionMessage(reason CallRejectionReason, blockedSelfService bool) (string, bool) {
	if blockedSelfService && reason != RejectionBlock {
		return "", false
	}
	switch reason {
	case RejectionInvalidParams:
		return "Request rejected: invalid tools/call parameters. Check the request shape.", true
	case RejectionUnknownTool:
		return "Request rejected: unknown tool. Refresh tools/list and check the tool name.", true
	case RejectionInvalidArguments:
		return "Request rejected: invalid tool arguments. Check the tool’s input schema.", true
	case RejectionDeny:
		return "DENIED: a matching DENY grant forbids this call. Additional ALLOW grants and self-service requests cannot override it.", true
	case RejectionBlock:
		if blockedSelfService {
			return "BLOCKED: no matching ALLOW grant authorizes this call. You may ask an administrator to review your access.", true
		}
		return "BLOCKED: no matching ALLOW grant authorizes this call. If available, you may use mcp_gateway.list_grants to inspect your access or mcp_gateway.create_grant_request to request access. Requesting access does not authorize the call; approval is required.", true
	case RejectionAuthorizationUnavailable:
		return "Call rejected: authorization could not be established. This is not a DENY or BLOCK decision.", true
	default:
		return "", false
	}
}

const AgentCallJSONRPCErrorCode = -32000

type AgentCallError struct {
	Code    AgentCallErrorCode
	Message string
}

var agentCallErrors = []AgentCallError{
	{Code: CallRejected, Message: "Call rejected"},
	{Code: AuditUnavailable, Message: "Call unavailable"},
	{Code: ToolUnavailable, Message: "Tool unavailable"},
	{Code: DownstreamFailure, Message: "Tool failed"},
	{Code: OutcomeUnknown, Message: "Tool outcome unknown"},
}

func AgentCallErrors() []AgentCallError {
	return append([]AgentCallError(nil), agentCallErrors...)
}

func AgentCallErrorForCode(code AgentCallErrorCode) (AgentCallError, bool) {
	for _, callError := range agentCallErrors {
		if callError.Code == code {
			return callError, true
		}
	}
	return AgentCallError{}, false
}
