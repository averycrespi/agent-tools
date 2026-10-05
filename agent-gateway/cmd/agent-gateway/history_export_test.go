package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHistoryExportCLIUsesOneBoundedAuthenticatedRead(t *testing.T) {
	value := contract.HistoryExport{Format: 1, InstallationID: auditTestID, Generation: auditTestID, CapturedAt: "2026-10-05T00:00:00Z", HighWater: "5", Pruning: "1", Retained: 0, AfterSequence: "5", NextSequence: "5", Absence: contract.HistoryExportAbsence, Records: []contract.HistoryExportRecord{}}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v2/history/export", r.URL.Path)
		require.Equal(t, "5", r.URL.Query().Get("after_sequence"))
		require.Equal(t, "256", r.URL.Query().Get("limit"))
		require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	defer server.Close()
	command := newRootCmd()
	var out, stderr bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(testAdministratorBearer + "\n"))
	command.SetArgs([]string{"history", "export", "--json", "--after-sequence", "5", "--limit", "256", "--address", server.URL, "--admin-bearer-stdin"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Equal(t, 1, calls)
	require.Empty(t, stderr.String())
	var actual contract.HistoryExport
	require.NoError(t, json.Unmarshal(out.Bytes(), &actual))
	require.Equal(t, value, actual)
}

func TestHistoryExportCLIRejectsInvalidBoundsBeforeAuthority(t *testing.T) {
	for _, args := range [][]string{{"--after-sequence", "-1"}, {"--after-sequence", "01"}, {"--after-sequence", ""}, {"--limit", "257"}, {"--limit", "0"}} {
		command := newRootCmd()
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetArgs(append([]string{"history", "export", "--json"}, args...))
		require.Error(t, command.ExecuteContext(t.Context()))
		require.Empty(t, out.String())
		require.Contains(t, stderr.String(), "client_invalid_input")
	}
}
