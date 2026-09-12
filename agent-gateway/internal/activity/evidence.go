// Package activity holds common retained evidence values. It owns no authority,
// persistence, execution, or public representation. The existing closed contract
// vocabulary remains in use; MCP is the only implemented activity domain.
package activity

import "github.com/averycrespi/agent-tools/agent-gateway/internal/contract"

type Identity struct {
	InvocationID string
	AdmittedAt   string
}

type Authorization struct {
	Decision              contract.AuthorizationDecision
	AuthorizationRevision string
	EvaluatedAt           string
	GrantID               *string
}

type Admission struct {
	PrincipalID           string
	CredentialID          string
	CredentialFingerprint string
	CredentialRevision    string
	Class                 contract.InvocationAdmissionClass
	Authorization         *Authorization
}

type Completion struct {
	CompletedAt string
	Class       contract.InvocationTerminalClass
}

// Envelope separates common facts from domain details. A nil Completion is
// missing evidence, never proof of nonexecution or permission to retry.
// Identity is prepared before admission; completion is a later annotation.
type Envelope struct {
	Identity
	Admission
	Completion *Completion
}
