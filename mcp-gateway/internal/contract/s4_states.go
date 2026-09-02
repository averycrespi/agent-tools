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
