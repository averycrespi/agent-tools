package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvocationClosedVocabularyAndLimitsAreExact(t *testing.T) {
	t.Parallel()

	require.Equal(t, []InvocationAdmissionClass{
		AdmissionInvalidParams,
		AdmissionUnknownTool,
		AdmissionInvalidArguments,
		AdmissionAuthorizationUnavailable,
		AdmissionEvaluated,
	}, InvocationAdmissionClasses())
	require.Equal(t, []InvocationTerminalClass{
		TerminalPrestartFailure,
		TerminalSucceeded,
		TerminalDownstreamFailure,
		TerminalOutcomeUnknown,
	}, InvocationTerminalClasses())
	require.Equal(t, []AgentCallError{
		{Code: CallRejected, Message: "Call rejected"},
		{Code: AuditUnavailable, Message: "Call unavailable"},
		{Code: ToolUnavailable, Message: "Tool unavailable"},
		{Code: DownstreamFailure, Message: "Tool failed"},
		{Code: OutcomeUnknown, Message: "Tool outcome unknown"},
	}, AgentCallErrors())
	require.Equal(t, -32000, AgentCallJSONRPCErrorCode)

	for _, value := range []string{"invalid_params", "unknown_tool", "invalid_arguments", "authorization_unavailable", "evaluated"} {
		parsed, err := ParseInvocationAdmissionClass(value)
		require.NoError(t, err)
		require.Equal(t, value, string(parsed))
	}
	_, err := ParseInvocationAdmissionClass("allow")
	require.Error(t, err)
	for _, value := range []string{"prestart_failure", "succeeded", "downstream_failure", "outcome_unknown"} {
		parsed, parseErr := ParseInvocationTerminalClass(value)
		require.NoError(t, parseErr)
		require.Equal(t, value, string(parsed))
	}
	_, err = ParseInvocationTerminalClass("dispatch_started")
	require.Error(t, err)
	for _, value := range []string{"call_rejected", "audit_unavailable", "tool_unavailable", "downstream_failure", "outcome_unknown"} {
		parsed, parseErr := ParseAgentCallErrorCode(value)
		require.NoError(t, parseErr)
		require.Equal(t, value, string(parsed))
	}
	_, err = ParseAgentCallErrorCode("raw_downstream_error")
	require.Error(t, err)

	rows, ok := FixedLimitByName("invocation_audit_rows")
	require.True(t, ok)
	require.Equal(t, int64(4096), rows.Maximum)
	require.True(t, rows.Allows(4096))
	require.False(t, rows.Allows(4097))
	capture, ok := FixedLimitByName("invocation_argument_capture_bytes")
	require.True(t, ok)
	require.Equal(t, int64(8192), capture.Maximum)
	require.True(t, capture.Allows(8192))
	require.False(t, capture.Allows(8193))
}

func TestInvocationAgentErrorDataShapeIsClosed(t *testing.T) {
	t.Parallel()

	invocationID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	encoded, err := json.Marshal(AgentCallErrorData{Code: OutcomeUnknown, InvocationID: &invocationID, OutcomeUnknown: true})
	require.NoError(t, err)
	require.JSONEq(t, `{"code":"outcome_unknown","invocationId":"01ARZ3NDEKTSV4RRFFQ69G5FAV","outcomeUnknown":true}`, string(encoded))

	encoded, err = json.Marshal(AgentCallErrorData{Code: CallRejected})
	require.NoError(t, err)
	require.JSONEq(t, `{"code":"call_rejected"}`, string(encoded))
}

func TestInvocationAuditRecordCarriesOnlyBoundedEvidence(t *testing.T) {
	t.Parallel()

	recordType := InvocationAuditRecord{
		InvocationID:          "invocation",
		PrincipalID:           "principal",
		CredentialID:          "credential",
		CredentialFingerprint: "fingerprint",
		CredentialRevision:    "7",
		AdmittedAt:            "2026-08-26T00:00:00Z",
		AdmissionClass:        AdmissionEvaluated,
	}
	require.NotEmpty(t, recordType.InvocationID)
	require.Equal(t, AdmissionEvaluated, recordType.AdmissionClass)
}
