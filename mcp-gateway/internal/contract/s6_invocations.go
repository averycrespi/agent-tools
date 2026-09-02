package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type InvocationTargetKind string

const (
	InvocationTargetDownstream InvocationTargetKind = "downstream"
	InvocationTargetGateway    InvocationTargetKind = "gateway"
)

func InvocationTargetKinds() []InvocationTargetKind {
	return []InvocationTargetKind{InvocationTargetDownstream, InvocationTargetGateway}
}

func ParseInvocationTargetKind(value string) (InvocationTargetKind, error) {
	return parseClosed(value, InvocationTargetKinds())
}

type InvocationOutcomeClass string

const (
	InvocationOutcomeInvalidParams            InvocationOutcomeClass = "invalid_params"
	InvocationOutcomeUnknownTool              InvocationOutcomeClass = "unknown_tool"
	InvocationOutcomeInvalidArguments         InvocationOutcomeClass = "invalid_arguments"
	InvocationOutcomeAuthorizationUnavailable InvocationOutcomeClass = "authorization_unavailable"
	InvocationOutcomeDeny                     InvocationOutcomeClass = "deny"
	InvocationOutcomeBlock                    InvocationOutcomeClass = "block"
	InvocationOutcomePrestartFailure          InvocationOutcomeClass = "prestart_failure"
	InvocationOutcomeSucceeded                InvocationOutcomeClass = "succeeded"
	InvocationOutcomeDownstreamFailure        InvocationOutcomeClass = "downstream_failure"
	InvocationOutcomeUnknown                  InvocationOutcomeClass = "outcome_unknown"
)

func InvocationOutcomeClasses() []InvocationOutcomeClass {
	return []InvocationOutcomeClass{
		InvocationOutcomeInvalidParams,
		InvocationOutcomeUnknownTool,
		InvocationOutcomeInvalidArguments,
		InvocationOutcomeAuthorizationUnavailable,
		InvocationOutcomeDeny,
		InvocationOutcomeBlock,
		InvocationOutcomePrestartFailure,
		InvocationOutcomeSucceeded,
		InvocationOutcomeDownstreamFailure,
		InvocationOutcomeUnknown,
	}
}

func ParseInvocationOutcomeClass(value string) (InvocationOutcomeClass, error) {
	return parseClosed(value, InvocationOutcomeClasses())
}

type InvocationOutcomeBasis string

const (
	InvocationBasisAdmission       InvocationOutcomeBasis = "admission"
	InvocationBasisPolicy          InvocationOutcomeBasis = "policy"
	InvocationBasisTerminal        InvocationOutcomeBasis = "terminal"
	InvocationBasisMissingTerminal InvocationOutcomeBasis = "missing_terminal"
)

func InvocationOutcomeBases() []InvocationOutcomeBasis {
	return []InvocationOutcomeBasis{InvocationBasisAdmission, InvocationBasisPolicy, InvocationBasisTerminal, InvocationBasisMissingTerminal}
}

func ParseInvocationOutcomeBasis(value string) (InvocationOutcomeBasis, error) {
	return parseClosed(value, InvocationOutcomeBases())
}

type InvocationTarget struct {
	Kind                  InvocationTargetKind `json:"kind"`
	ServerID              string               `json:"server_id"`
	ToolID                string               `json:"tool_id"`
	UpstreamName          string               `json:"upstream_name"`
	DescriptorRevision    string               `json:"descriptor_revision"`
	DescriptorFingerprint string               `json:"descriptor_fingerprint"`
}

type InvocationAuthorization struct {
	Decision    AuthorizationDecision `json:"decision"`
	Revision    string                `json:"revision"`
	EvaluatedAt string                `json:"evaluated_at"`
	GrantID     *string               `json:"grant_id"`
}

type InvocationOutcome struct {
	Class       InvocationOutcomeClass `json:"class"`
	Basis       InvocationOutcomeBasis `json:"basis"`
	CompletedAt *string                `json:"completed_at"`
}

type InvocationSummary struct {
	ID                    string                   `json:"id"`
	PrincipalID           string                   `json:"principal_id"`
	CredentialID          string                   `json:"credential_id"`
	CredentialFingerprint string                   `json:"credential_fingerprint"`
	CredentialRevision    string                   `json:"credential_revision"`
	AdmittedAt            string                   `json:"admitted_at"`
	AdmissionClass        InvocationAdmissionClass `json:"admission_class"`
	RequestedName         *string                  `json:"requested_name"`
	Target                *InvocationTarget        `json:"target"`
	Authorization         *InvocationAuthorization `json:"authorization"`
	Outcome               InvocationOutcome        `json:"outcome"`
}

type Invocation struct {
	InvocationSummary
	RedactedArguments json.RawMessage `json:"redacted_arguments"`
}

type InvocationFilters struct {
	PrincipalID    *string                   `json:"principal_id"`
	ServerID       *string                   `json:"server_id"`
	RequestedName  *string                   `json:"requested_name"`
	AdmissionClass *InvocationAdmissionClass `json:"admission_class"`
	Decision       *AuthorizationDecision    `json:"decision"`
	Outcome        *InvocationOutcomeClass   `json:"outcome"`
}

type InvocationListQuery struct {
	Cursor  *string           `json:"cursor"`
	Limit   int               `json:"limit"`
	Filters InvocationFilters `json:"filters"`
}

type InvocationPage struct {
	Items      []InvocationSummary `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type InvocationCursorBinding struct {
	Filters       InvocationFilters
	UpperSequence int64
	NextSequence  int64
}

func ProjectInvocationAudit(record InvocationAuditRecord) (Invocation, error) {
	summary, err := ProjectInvocationSummary(record)
	if err != nil {
		return Invocation{}, err
	}
	arguments, err := projectRedactedArguments(record.RedactedArguments)
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{InvocationSummary: summary, RedactedArguments: arguments}, nil
}

func ProjectInvocationSummary(record InvocationAuditRecord) (InvocationSummary, error) {
	if _, err := ParseInvocationAdmissionClass(string(record.AdmissionClass)); err != nil {
		return InvocationSummary{}, fmt.Errorf("invalid invocation admission class: %w", err)
	}
	target, err := projectInvocationTarget(record)
	if err != nil {
		return InvocationSummary{}, err
	}
	authorization, outcome, err := projectInvocationOutcome(record)
	if err != nil {
		return InvocationSummary{}, err
	}
	return InvocationSummary{
		ID: record.InvocationID, PrincipalID: record.PrincipalID, CredentialID: record.CredentialID,
		CredentialFingerprint: record.CredentialFingerprint, CredentialRevision: record.CredentialRevision,
		AdmittedAt: record.AdmittedAt, AdmissionClass: record.AdmissionClass, RequestedName: record.RequestedName,
		Target: target, Authorization: authorization, Outcome: outcome,
	}, nil
}

func projectInvocationTarget(record InvocationAuditRecord) (*InvocationTarget, error) {
	members := []*string{record.ServerID, record.ToolID, record.UpstreamName, record.DescriptorRevision, record.DescriptorFingerprint}
	present := 0
	for _, member := range members {
		if member != nil {
			present++
		}
	}
	if present == 0 {
		return nil, nil
	}
	if present != len(members) {
		return nil, fmt.Errorf("invocation target evidence is incomplete")
	}
	kind := InvocationTargetDownstream
	if *record.ServerID == SyntheticServerID {
		kind = InvocationTargetGateway
	}
	return &InvocationTarget{
		Kind: kind, ServerID: *record.ServerID, ToolID: *record.ToolID, UpstreamName: *record.UpstreamName,
		DescriptorRevision: *record.DescriptorRevision, DescriptorFingerprint: *record.DescriptorFingerprint,
	}, nil
}

func projectInvocationOutcome(record InvocationAuditRecord) (*InvocationAuthorization, InvocationOutcome, error) {
	terminalPresent := record.CompletedAt != nil || record.TerminalClass != nil
	if (record.CompletedAt == nil) != (record.TerminalClass == nil) {
		return nil, InvocationOutcome{}, fmt.Errorf("invocation terminal evidence is incomplete")
	}
	if record.AdmissionClass != AdmissionEvaluated {
		if record.AuthorizationDecision != nil || record.AuthorizationRevision != nil || record.EvaluatedAt != nil || record.GrantID != nil || terminalPresent {
			return nil, InvocationOutcome{}, fmt.Errorf("nonevaluated invocation contains evaluated evidence")
		}
		class, ok := admissionOutcome(record.AdmissionClass)
		if !ok {
			return nil, InvocationOutcome{}, fmt.Errorf("evaluated invocation is missing authorization evidence")
		}
		return nil, InvocationOutcome{Class: class, Basis: InvocationBasisAdmission}, nil
	}
	if record.AuthorizationDecision == nil || record.AuthorizationRevision == nil || record.EvaluatedAt == nil {
		return nil, InvocationOutcome{}, fmt.Errorf("evaluated invocation authorization evidence is incomplete")
	}
	if _, err := ParseAuthorizationDecision(string(*record.AuthorizationDecision)); err != nil {
		return nil, InvocationOutcome{}, fmt.Errorf("invalid invocation decision: %w", err)
	}
	authorization := &InvocationAuthorization{
		Decision: *record.AuthorizationDecision, Revision: *record.AuthorizationRevision,
		EvaluatedAt: *record.EvaluatedAt, GrantID: record.GrantID,
	}
	switch *record.AuthorizationDecision {
	case DecisionDeny:
		if terminalPresent {
			return nil, InvocationOutcome{}, fmt.Errorf("denied invocation contains terminal evidence")
		}
		return authorization, InvocationOutcome{Class: InvocationOutcomeDeny, Basis: InvocationBasisPolicy}, nil
	case DecisionBlock:
		if terminalPresent {
			return nil, InvocationOutcome{}, fmt.Errorf("blocked invocation contains terminal evidence")
		}
		return authorization, InvocationOutcome{Class: InvocationOutcomeBlock, Basis: InvocationBasisPolicy}, nil
	case DecisionAllow:
		if !terminalPresent {
			return authorization, InvocationOutcome{Class: InvocationOutcomeUnknown, Basis: InvocationBasisMissingTerminal}, nil
		}
		class, ok := terminalOutcome(*record.TerminalClass)
		if !ok {
			return nil, InvocationOutcome{}, fmt.Errorf("invalid invocation terminal class %q", *record.TerminalClass)
		}
		return authorization, InvocationOutcome{Class: class, Basis: InvocationBasisTerminal, CompletedAt: record.CompletedAt}, nil
	default:
		return nil, InvocationOutcome{}, fmt.Errorf("invalid invocation decision %q", *record.AuthorizationDecision)
	}
}

func admissionOutcome(class InvocationAdmissionClass) (InvocationOutcomeClass, bool) {
	switch class {
	case AdmissionInvalidParams:
		return InvocationOutcomeInvalidParams, true
	case AdmissionUnknownTool:
		return InvocationOutcomeUnknownTool, true
	case AdmissionInvalidArguments:
		return InvocationOutcomeInvalidArguments, true
	case AdmissionAuthorizationUnavailable:
		return InvocationOutcomeAuthorizationUnavailable, true
	default:
		return "", false
	}
}

func terminalOutcome(class InvocationTerminalClass) (InvocationOutcomeClass, bool) {
	switch class {
	case TerminalPrestartFailure:
		return InvocationOutcomePrestartFailure, true
	case TerminalSucceeded:
		return InvocationOutcomeSucceeded, true
	case TerminalDownstreamFailure:
		return InvocationOutcomeDownstreamFailure, true
	case TerminalOutcomeUnknown:
		return InvocationOutcomeUnknown, true
	default:
		return "", false
	}
}

func projectRedactedArguments(value *string) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}
	encoded := []byte(*value)
	if !json.Valid(encoded) {
		return nil, fmt.Errorf("redacted arguments are invalid JSON")
	}
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 || (trimmed[0] != '{' && string(trimmed) != `"[TRUNCATED]"`) {
		return nil, fmt.Errorf("redacted arguments have an invalid shape")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}
