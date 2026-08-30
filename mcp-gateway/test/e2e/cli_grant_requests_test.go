//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCLIGrantRequestInputMatrix(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	harness.SetupCurrentCatalog("request-target", []fixtureTool{
		{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "one", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "two", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	principal := harness.CreatePrincipal("Request principal", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	defer issued.Bearer.destroy()
	createRequest := func(id json.RawMessage, policy contract.Policy) contract.AgentGrantRequest {
		response := harness.ModernSelfServiceCall(issued.Bearer, id, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy})
		created := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, response, id, contract.SummaryGrantRequestProcessed)
		require.NotNil(t, created.Request)
		return *created.Request
	}
	serverPolicy := contract.Policy{Scope: contract.PolicyServer, Target: "request-target", FutureToolsAcknowledged: true}
	first := createRequest(json.RawMessage(`1`), serverPolicy)
	second := createRequest(json.RawMessage(`2`), contract.Policy{Scope: contract.PolicyTool, Target: "request-target.one"})
	third := createRequest(json.RawMessage(`3`), contract.Policy{Scope: contract.PolicyTool, Target: "request-target.two"})
	fourth := createRequest(json.RawMessage(`4`), contract.Policy{Scope: contract.PolicyTool, Target: "request-target.echo"})
	firstHandle := harness.GetGrantRequest(first.ID)
	secondHandle := harness.GetGrantRequest(second.ID)
	thirdHandle := harness.GetGrantRequest(third.ID)
	dir := t.TempDir()
	approvePath := filepath.Join(dir, "approve.json")
	invalidPath := filepath.Join(dir, "invalid.json")
	approveBody := `{"approved_policy":{"scope":"tool","target":"request-target.echo","constraint":null,"duration_seconds":null,"future_tools_acknowledged":false}}`
	require.NoError(t, os.WriteFile(approvePath, []byte(approveBody), 0o600))
	require.NoError(t, os.WriteFile(invalidPath, []byte(`{"approved_policy":{"scope":"server","target":"mcp_gateway","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true,"unknown":true}}`), 0o600))
	results := make([]testutil.ProcessResult, 0, 10)

	listed := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "list", "--principal-id", principal.Resource.ID, "--state", "pending", "--limit", "10", "--output", "json")
	got := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "get", first.ID)
	results = append(results, listed, got)
	assert.Contains(t, string(listed.Stdout), first.ID)
	assert.Contains(t, string(got.Stdout), "CURRENT_TARGET")

	invalid := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "approve", first.ID, "--etag", firstHandle.ETag, "--file", invalidPath, "--yes", "--output", "json")
	oldReject := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "reject", second.ID, "--file", invalidPath, "--yes", "--output", "json")
	results = append(results, invalid, oldReject)
	assert.Equal(t, 2, invalid.ExitCode)
	assert.Equal(t, 2, oldReject.ExitCode)
	refused := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "approve", first.ID, "--etag", firstHandle.ETag, "--file", approvePath, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	approved := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "approve", first.ID, "--etag", firstHandle.ETag, "--file", approvePath, "--yes", "--output", "json")
	results = append(results, approved)
	var approvedRequest contract.GrantRequest
	require.NoError(t, json.Unmarshal(approved.Stdout, &approvedRequest))
	assert.Equal(t, contract.RequestApproved, approvedRequest.State)
	require.NotNil(t, approvedRequest.ApprovedGrantID)
	directApproved := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "approve", fourth.ID, "--scope", "tool", "--target", "request-target.echo", "--yes", "--output", "json")
	results = append(results, directApproved)
	var directApprovedRequest contract.GrantRequest
	require.NoError(t, json.Unmarshal(directApproved.Stdout, &directApprovedRequest))
	assert.Equal(t, contract.RequestApproved, directApprovedRequest.State)

	stale := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "reject", first.ID, "--etag", firstHandle.ETag, "--reason", "scope_too_broad", "--yes", "--output", "json")
	terminal := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "approve", first.ID, "--etag", contract.GrantRequestETag(first.ID, approvedRequest.Revision), "--file", approvePath, "--yes", "--output", "json")
	results = append(results, stale, terminal)
	assert.Equal(t, 5, stale.ExitCode)
	assert.Equal(t, 5, terminal.ExitCode)

	rejected := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "reject", second.ID, "--etag", secondHandle.ETag, "--reason", "scope_too_broad", "--yes", "--output", "json")
	results = append(results, rejected)
	var rejectedRequest contract.GrantRequest
	require.NoError(t, json.Unmarshal(rejected.Stdout, &rejectedRequest))
	assert.Equal(t, contract.RequestRejected, rejectedRequest.State)
	assert.Equal(t, contract.RejectionScopeTooBroad, *rejectedRequest.RejectionReason)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"authorization_unavailable","title":"Authorization is unavailable."}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "grant-request", "reject", third.ID, "--etag", thirdHandle.ETag, "--reason", "scope_too_broad", "--yes", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.Contains(t, string(uncertain.Stderr), "Nothing was replayed")

	harness.Stop(syscall.SIGTERM)
	preHandoff := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "get", third.ID, "--output", "json")
	results = append(results, preHandoff)
	assert.Equal(t, 9, preHandoff.ExitCode)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T49 must own one Gateway lifecycle")
}
