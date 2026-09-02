package invocation

import (
	"encoding/base64"
	"encoding/json"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

const invocationCursorVersion = 1

type invocationCursor struct {
	Version        int                                `json:"v"`
	PrincipalID    *string                            `json:"p,omitempty"`
	ServerID       *string                            `json:"s,omitempty"`
	RequestedName  *string                            `json:"r,omitempty"`
	AdmissionClass *contract.InvocationAdmissionClass `json:"a,omitempty"`
	Decision       *contract.AuthorizationDecision    `json:"d,omitempty"`
	Outcome        *contract.InvocationOutcomeClass   `json:"o,omitempty"`
	UpperSequence  int64                              `json:"u"`
	NextSequence   int64                              `json:"n"`
}

func encodeInvocationCursor(binding contract.InvocationCursorBinding) (string, error) {
	if !validInvocationFilters(binding.Filters) || binding.UpperSequence <= 0 || binding.NextSequence <= 0 || binding.NextSequence > binding.UpperSequence {
		return "", ErrInvalidCursor
	}
	contents, err := json.Marshal(invocationCursor{
		Version:     invocationCursorVersion,
		PrincipalID: binding.Filters.PrincipalID, ServerID: binding.Filters.ServerID, RequestedName: binding.Filters.RequestedName,
		AdmissionClass: binding.Filters.AdmissionClass, Decision: binding.Filters.Decision, Outcome: binding.Filters.Outcome,
		UpperSequence: binding.UpperSequence, NextSequence: binding.NextSequence,
	})
	if err != nil {
		return "", ErrInvalidCursor
	}
	encoded := base64.RawURLEncoding.EncodeToString(contents)
	if int64(len(encoded)) > invocationCursorLimit() {
		return "", ErrInvalidCursor
	}
	return encoded, nil
}

func decodeInvocationCursor(value string) (contract.InvocationCursorBinding, error) {
	if value == "" || int64(len(value)) > invocationCursorLimit() {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	contents, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(contents) != value {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	var decoded invocationCursor
	if strictjson.Decode(contents, &decoded, strictjson.Options{
		MaxBytes: invocationCursorLimit(), MaxDepth: jsonDepthLimit(), RejectUnknownMembers: true,
	}) != nil || decoded.Version != invocationCursorVersion ||
		decoded.UpperSequence <= 0 || decoded.NextSequence <= 0 || decoded.NextSequence > decoded.UpperSequence {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	filters := contract.InvocationFilters{
		PrincipalID: decoded.PrincipalID, ServerID: decoded.ServerID, RequestedName: decoded.RequestedName,
		AdmissionClass: decoded.AdmissionClass, Decision: decoded.Decision, Outcome: decoded.Outcome,
	}
	if !validInvocationFilters(filters) {
		return contract.InvocationCursorBinding{}, ErrInvalidCursor
	}
	return contract.InvocationCursorBinding{
		Filters: filters, UpperSequence: decoded.UpperSequence, NextSequence: decoded.NextSequence,
	}, nil
}

func sameInvocationFilters(left, right contract.InvocationFilters) bool {
	return sameString(left.PrincipalID, right.PrincipalID) && sameString(left.ServerID, right.ServerID) &&
		sameString(left.RequestedName, right.RequestedName) && sameAdmission(left.AdmissionClass, right.AdmissionClass) &&
		sameDecision(left.Decision, right.Decision) && sameOutcome(left.Outcome, right.Outcome)
}

func sameString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameAdmission(left, right *contract.InvocationAdmissionClass) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameDecision(left, right *contract.AuthorizationDecision) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameOutcome(left, right *contract.InvocationOutcomeClass) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
