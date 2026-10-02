package main

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGitTrafficCLISeparatesTransportAndReport(t *testing.T) {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	item := contract.GitTrafficRecord{Admission: contract.GitTrafficAdmission{ID: id, AdmittedAt: "2026-09-30T12:00:00.000000000Z", EvaluatedAt: "2026-09-30T12:00:00.000000000Z", Principal: contract.GitRevisionRef{ID: id, Revision: "1"}, AgentCredential: contract.GitRevisionRef{ID: id, Revision: "1"}, Repository: contract.GitRevisionRef{ID: id, Revision: "1"}, AliasRevision: "1", ProfileRevision: "1", AuthorizationRevision: "1", Operation: "push", Commands: 1, Allowed: true}, Completion: &contract.GitTrafficCompletion{CompletedAt: "2026-09-30T12:00:00.000000000Z", Outcome: "outcome_unknown", Status: 200, TransferComplete: true}}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	table, err := gitTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"Transport", "Complete"})
	require.Contains(t, table.Rows, []string{"Upstream report", "Unknown"})
	item.Completion.ReportedResult = "reported_partial"
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	table, err = gitTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"Upstream report", "Reported partial success"})
	item.Completion.TransferComplete = false
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	_, err = gitTrafficItemTable(raw)
	require.Error(t, err)
	item.Completion = nil
	raw, err = json.Marshal(item)
	require.NoError(t, err)
	table, err = gitTrafficItemTable(raw)
	require.NoError(t, err)
	require.Contains(t, table.Rows, []string{"Transport", "Unknown"})
}
