package contract

const (
	SyntheticServerID        = "00000000000000000000000000"
	SyntheticServerNamespace = "mcp_gateway"
	DefaultGrantName         = "Default Gateway access"
)

type PrincipalState string

const (
	PrincipalActive   PrincipalState = "active"
	PrincipalDisabled PrincipalState = "disabled"
)

func PrincipalStates() []PrincipalState {
	return []PrincipalState{PrincipalActive, PrincipalDisabled}
}

func ParsePrincipalState(value string) (PrincipalState, error) {
	return parseClosed(value, PrincipalStates())
}

type PrincipalVisibility string

const (
	VisibilityRequestable PrincipalVisibility = "requestable"
	VisibilityAllowedOnly PrincipalVisibility = "allowed-only"
	VisibilityAll         PrincipalVisibility = "all"
)

func PrincipalVisibilities() []PrincipalVisibility {
	return []PrincipalVisibility{VisibilityRequestable, VisibilityAllowedOnly, VisibilityAll}
}

func ParsePrincipalVisibility(value string) (PrincipalVisibility, error) {
	return parseClosed(value, PrincipalVisibilities())
}

type GrantEffect string

const (
	GrantAllow GrantEffect = "allow"
	GrantDeny  GrantEffect = "deny"
)

func GrantEffects() []GrantEffect {
	return []GrantEffect{GrantAllow, GrantDeny}
}

func ParseGrantEffect(value string) (GrantEffect, error) {
	return parseClosed(value, GrantEffects())
}

type GrantState string

const (
	GrantActive  GrantState = "active"
	GrantExpired GrantState = "expired"
)

func GrantStates() []GrantState {
	return []GrantState{GrantActive, GrantExpired}
}

func ParseGrantState(value string) (GrantState, error) {
	return parseClosed(value, GrantStates())
}

type AuthorizationDecision string

const (
	DecisionAllow AuthorizationDecision = "allow"
	DecisionDeny  AuthorizationDecision = "deny"
	DecisionBlock AuthorizationDecision = "block"
)

func AuthorizationDecisions() []AuthorizationDecision {
	return []AuthorizationDecision{DecisionAllow, DecisionDeny, DecisionBlock}
}

func ParseAuthorizationDecision(value string) (AuthorizationDecision, error) {
	return parseClosed(value, AuthorizationDecisions())
}
