package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const auditTestID = "00000000000000000000000001"

func auditTestEvent() contract.AuditEvent {
	return contract.AuditEvent{AuditSummary: contract.AuditSummary{ID: auditTestID, Sequence: "2", Timestamp: "2026-09-05T00:00:00.000000000Z", Category: "server", Action: "reconcile", Phase: "outcome", Outcome: "unknown", Actor: contract.AuditActor{Type: contract.AuditSystem}, Initiator: &contract.AuditCredential{ID: auditTestID, Fingerprint: "0123456789abcdef"}, CorrelationID: auditTestID, Target: contract.AuditTarget{Type: "server", ID: auditTestID}}}
}
func auditTestHistory() contract.AuditHistory {
	return contract.AuditHistory{Generation: strings.Repeat("a", 64), OldestRetained: &contract.AuditBoundary{ID: auditTestID, Sequence: "1", Timestamp: "2026-09-04T00:00:00.000000000Z"}, Pruned: true}
}
func executeAudit(t *testing.T, address string, args ...string) (string, string, error) {
	t.Helper()
	command := newRootCmd()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(testAdministratorBearer + "\n"))
	command.SetArgs(append(append([]string{"audit"}, args...), "--address", address, "--admin-bearer-stdin"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := command.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}
func TestAuditCLITransportRepresentationAndDetail(t *testing.T) {
	event := auditTestEvent()
	cursor := "opaque+/=?cursor"
	page := contract.AuditPage{Items: []contract.AuditSummary{event.AuditSummary}, NextCursor: &cursor, History: auditTestHistory()}
	item := contract.AuditItem{Event: event, History: page.History}
	pageBody, err := json.Marshal(page)
	require.NoError(t, err)
	itemBody, err := json.Marshal(item)
	require.NoError(t, err)
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(r.Context()))
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
		assert.Equal(t, int64(0), r.ContentLength)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/audit-events/"+auditTestID {
			_, _ = w.Write(itemBody)
		} else {
			_, _ = w.Write(pageBody)
		}
	}))
	defer server.Close()
	args := []string{"list", "--json", "--limit", "1", "--cursor", cursor, "--generation", page.History.Generation, "--actor-type", "system", "--credential-id", auditTestID, "--category", "server", "--action", "reconcile", "--target-type", "server", "--target-id", auditTestID, "--outcome", "unknown", "--correlation-id", auditTestID, "--from", "2026-09-04T00:00:00.000000000Z", "--until", "2026-09-06T00:00:00.000000000Z"}
	stdout, stderr, err := executeAudit(t, server.URL, args...)
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, string(pageBody)+"\n", stdout)
	require.Len(t, requests, 1)
	expected := map[string]string{"limit": "1", "cursor": cursor, "generation": page.History.Generation, "actor_type": "system", "credential_id": auditTestID, "category": "server", "action": "reconcile", "target_type": "server", "target_id": auditTestID, "outcome": "unknown", "correlation_id": auditTestID, "from": "2026-09-04T00:00:00.000000000Z", "until": "2026-09-06T00:00:00.000000000Z"}
	assert.Len(t, requests[0].URL.Query(), len(expected))
	for key, value := range expected {
		assert.Equal(t, value, requests[0].URL.Query().Get(key))
	}
	stdout, stderr, err = executeAudit(t, server.URL, "get", auditTestID, "--generation", page.History.Generation, "--json")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, string(itemBody)+"\n", stdout)
	stdout, stderr, err = executeAudit(t, server.URL, "get", auditTestID)
	require.NoError(t, err)
	assert.Empty(t, stderr)
	for _, text := range []string{"system", "INITIATOR", "unknown", "Reason:", "Problem:", "Oldest retained:", "pruned=true", "65,536", "Restore may replace", "not performers", page.History.Generation} {
		assert.Contains(t, stdout, text)
	}
}
func TestAuditCLIRejectsMalformedEvidenceAndDoesNotReplay(t *testing.T) {
	page := contract.AuditPage{Items: []contract.AuditSummary{auditTestEvent().AuditSummary}, History: auditTestHistory()}
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	good := string(encoded)
	for name, body := range map[string]string{
		"unknown member":   strings.Replace(good, `"items":`, `"secret":"not-allowed","items":`, 1),
		"missing nullable": strings.Replace(good, `"next_cursor":null,`, "", 1),
		"duplicate member": strings.Replace(good, `"pruned":true`, `"pruned":true,"pruned":true`, 1),
		"null scalar":      strings.Replace(good, `"pruned":true`, `"pruned":null`, 1),
		"bad actor":        strings.Replace(good, `"type":"system"`, `"type":"human"`, 1),
		"wrong generation": strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("b", 64), 1),
		"numeric sequence": strings.Replace(good, `"sequence":"2"`, `"sequence":2`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			stdout, stderr, err := executeAudit(t, server.URL, "list", "--generation", strings.Repeat("a", 64), "--json")
			require.Error(t, err)
			assert.Equal(t, 10, commandExitCode(err))
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, "client_response_invalid")
			assert.Equal(t, 1, calls)
		})
	}
	for _, code := range []string{"stale_cursor", "audit_history_replaced", "storage_unavailable", "not_found"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			problem, ok := contract.ProblemForCode(contract.ProblemCode(code))
			require.True(t, ok)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(problem.Status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": problem.Status, "code": code, "title": problem.Title})
			}))
			defer server.Close()
			stdout, stderr, err := executeAudit(t, server.URL, "list", "--json")
			require.Error(t, err)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, code)
			assert.Equal(t, 1, calls)
		})
	}
}
func TestAuditCLIInvalidFiltersBeforeCredentialRead(t *testing.T) {
	for _, args := range [][]string{{"list", "--actor-type", "human"}, {"list", "--credential-id", "bad"}, {"list", "--category", "server", "--action", "rotate"}, {"list", "--from", "2026-09-05T00:00:00.000000000Z"}, {"list", "--generation", "bad"}, {"list", "--outcome", ""}, {"list", "--cursor", ""}, {"get"}} {
		command := newRootCmd()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetIn(strings.NewReader(""))
		command.SetArgs(append([]string{"audit"}, args...))
		err := command.ExecuteContext(context.Background())
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err))
		assert.NotContains(t, output.String(), "bearer")
	}
}
