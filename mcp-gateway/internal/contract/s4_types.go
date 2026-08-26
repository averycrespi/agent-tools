package contract

type AgentCallErrorData struct {
	Code           AgentCallErrorCode `json:"code"`
	InvocationID   *string            `json:"invocationId,omitempty"`
	OutcomeUnknown bool               `json:"outcomeUnknown,omitempty"`
}

type InvocationAuditRecord struct {
	Sequence              int64
	InvocationID          string
	PrincipalID           string
	CredentialID          string
	CredentialFingerprint string
	CredentialRevision    string
	AdmittedAt            string
	AdmissionClass        InvocationAdmissionClass
	RequestedName         *string
	RedactedArguments     *string
	ServerID              *string
	ToolID                *string
	UpstreamName          *string
	DescriptorRevision    *string
	DescriptorFingerprint *string
	AuthorizationDecision *AuthorizationDecision
	AuthorizationRevision *string
	EvaluatedAt           *string
	GrantID               *string
	CompletedAt           *string
	TerminalClass         *InvocationTerminalClass
}
