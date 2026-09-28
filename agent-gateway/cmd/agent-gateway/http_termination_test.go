package main

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLIHTTPTrafficTerminationBoundaries(t *testing.T) {
	ref := contract.HTTPRevisionRef{ID: idForSecurityTest(), Revision: 1}
	at := "2026-09-21T00:00:00.000000000Z"
	target := &contract.HTTPTrafficTarget{Host: "example.com", Port: 443, Scheme: "https", Method: "GET"}
	observed := &contract.HTTPTermination{Stage: "upstream_read", Condition: "cancelled", Context: "cancelled"}
	completion := &contract.HTTPTrafficCompletion{CompletedAt: at, Outcome: "outcome_unknown", Status: 200, ResponseSource: "upstream", Termination: observed}
	item := contract.HTTPTrafficRecord{Admission: contract.HTTPTrafficAdmission{ID: ref.ID, Principal: ref, AgentCredential: ref, CredentialFingerprint: "0123456789abcdef", AdmittedAt: at, EvaluatedAt: at, Class: "evaluated", Default: contract.HTTPDefaultAllow, Target: target, Decision: &contract.HTTPDecision{Version: 1, Principal: ref, PolicyRevision: 1, DefaultRevision: 1, Allowed: true, Transport: contract.HTTPTransportRequest, Reason: contract.HTTPReasonDefault}, Grants: []contract.HTTPTrafficGrant{}}, Completion: completion}
	summary := contract.HTTPTrafficSummary{ID: ref.ID, AdmittedAt: at, PrincipalID: ref.ID, Target: target, Type: "request", Decision: "allow", Outcome: "outcome_unknown", ResponseSource: "upstream", CompletionRecorded: true, Termination: observed}
	require.True(t, validHTTPTrafficItem(item))
	require.True(t, validHTTPTrafficSummary(summary))
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	table, err := httpTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"Observed stage", "upstream_read"})
	require.Contains(t, table.Rows, []string{"Request context", "cancelled"})
	for _, bad := range []contract.HTTPTermination{{Stage: "client_cancelled", Condition: "cancelled"}, {Stage: "complete", Condition: "clean"}, {Stage: "upstream_read", Condition: "failure", Context: "client"}} {
		*observed = bad
		require.False(t, validHTTPTrafficItem(item))
		require.False(t, validHTTPTrafficSummary(summary))
	}
	completion.Termination = nil
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	table, err = httpTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"HTTP transfer", "Termination details unavailable"})
}
