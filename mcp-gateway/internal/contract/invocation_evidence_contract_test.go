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

func TestInvocationAcceptanceManifestsAreCompleteAndCopySafe(t *testing.T) {
	t.Parallel()

	criteria := S4AcceptanceEvidenceManifest()
	require.Equal(t, []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5"}, criterionNames(criteria))
	seenEvidence := map[string]bool{}
	for _, entry := range criteria {
		require.NotEmpty(t, entry.Evidence)
		for _, evidence := range entry.Evidence {
			require.Regexp(t, `^s4-[a-z0-9-]+$`, evidence)
			seenEvidence[evidence] = true
		}
	}
	criteria[0].Criterion = "changed"
	criteria[0].Evidence[0] = "changed"
	require.Equal(t, "AC-1", S4AcceptanceEvidenceManifest()[0].Criterion)
	require.NotEqual(t, "changed", S4AcceptanceEvidenceManifest()[0].Evidence[0])

	clauses := S4ClauseEvidenceManifest()
	require.Equal(t, requiredS4ClauseIDs(), clauseNames(clauses))
	seenTasks := map[string]bool{}
	for _, clause := range clauses {
		require.NotEmpty(t, clause.Tasks, clause.Clause)
		require.NotEmpty(t, clause.Evidence, clause.Clause)
		for _, task := range clause.Tasks {
			require.Regexp(t, `^T(?:[1-9]|1[0-9]|2[01])$`, task)
			seenTasks[task] = true
		}
		for _, evidence := range clause.Evidence {
			require.True(t, seenEvidence[evidence], "%s references criterion-unmapped evidence %s", clause.Clause, evidence)
		}
	}
	for task := 1; task <= 21; task++ {
		require.True(t, seenTasks["T"+itoa(task)], "T%d has no clause evidence assignment", task)
	}

	clauses[0].Clause = "changed"
	clauses[0].Tasks[0] = "changed"
	clauses[0].Evidence[0] = "changed"
	fresh := S4ClauseEvidenceManifest()[0]
	require.NotEqual(t, "changed", fresh.Clause)
	require.NotEqual(t, "changed", fresh.Tasks[0])
	require.NotEqual(t, "changed", fresh.Evidence[0])
}

func criterionNames(entries []AcceptanceEvidence) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Criterion
	}
	return result
}

func clauseNames(entries []ClauseEvidence) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Clause
	}
	return result
}

func requiredS4ClauseIDs() []string {
	result := []string{"OUTCOME-1", "SCOPE-1", "NG-1"}
	for section, count := range []int{6, 7, 5, 5, 3} {
		for clause := 1; clause <= count; clause++ {
			result = append(result, "RB-"+itoa(section+1)+"."+itoa(clause))
		}
	}
	for _, prefixAndCount := range []struct {
		prefix string
		count  int
	}{{"DI", 4}, {"FP", 5}, {"SO", 4}} {
		for clause := 1; clause <= prefixAndCount.count; clause++ {
			result = append(result, prefixAndCount.prefix+"-"+itoa(clause))
		}
	}
	return append(result, "COMP-1", "ARCH-1")
}

func itoa(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return "" + string(rune('0'+value/10)) + string(rune('0'+value%10))
}
