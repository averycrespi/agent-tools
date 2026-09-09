//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserReadOnlyAgentCLIAndBrowserEnforcement(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("readonlyclients", []fixtureTool{
		{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: json.RawMessage(`{"readOnlyHint":true}`)},
		{Name: "write", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: json.RawMessage(`{"readOnlyHint":false}`)},
		{Name: "missing", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	principal := harness.CreatePrincipal("Read-only client agent", contract.VisibilityAllowedOnly)
	issued := harness.IssueCredential(principal)
	defer issued.Bearer.destroy()
	create := func(readOnly bool) contract.AgentGrantRequest {
		policy := contract.Policy{Scope: contract.PolicyServer, Target: catalog.Namespace, FutureToolsAcknowledged: true, ReadOnly: readOnly}
		callID := json.RawMessage(`"request"`)
		result := decodeSelfServiceResult[contract.CreateGrantRequestResult](t, harness.ModernSelfServiceCall(issued.Bearer, callID, "create_grant_request", contract.CreateGrantRequestInput{Policy: policy}), callID, contract.SummaryGrantRequestProcessed)
		require.NotNil(t, result.Request)
		return *result.Request
	}
	cliRequest := create(true)
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0600))
	inspected := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "get", cliRequest.ID)
	require.Contains(t, string(inspected.Stdout), "read-only server tools")
	refused := runOnlineCLI(t, harness, bearerPath, false, "grant-request", "approve", cliRequest.ID, "--scope", "tool", "--target", catalog.Namespace+".read", "--yes", "--json")
	require.Equal(t, 2, refused.ExitCode)
	require.Equal(t, contract.RequestPending, harness.GetGrantRequest(cliRequest.ID).Resource.State)
	result := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "approve", cliRequest.ID, "--scope", "server", "--target", catalog.Namespace, "--read-only", "--acknowledge-future-tools", "--yes", "--json")
	var cliApproved contract.GrantRequest
	require.NoError(t, json.Unmarshal(result.Stdout, &cliApproved))
	require.True(t, cliApproved.ApprovedPolicy.ReadOnly)
	require.True(t, harness.GetGrant(*cliApproved.ApprovedGrantID).ReadOnly)
	// Remove the CLI grant so browser-created authority alone owns the enforcement check.
	harness.DeleteGrant(*cliApproved.ApprovedGrantID)
	browserRequest := create(false)
	runner, err := testutil.NewBinaryRunner(75*time.Second, 32*1024)
	require.NoError(t, err)
	process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	finished := false
	t.Cleanup(func() {
		if !finished {
			require.NoError(t, process.Stop())
			result, _ := process.Wait()
			require.True(t, result.Cleanup.Reaped)
			require.False(t, result.Cleanup.Survived)
		}
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{"version": 1, "scenario": "read-only-backend", "base_url": "http://" + harness.authority, "admin_bearer": harness.bearer}))
	require.NoError(t, input.Close())
	browserResult, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "%s", browserResult.Stderr)
	require.Empty(t, browserResult.Stderr)
	require.False(t, browserResult.StdoutTruncated)
	require.True(t, browserResult.Cleanup.Reaped)
	require.False(t, browserResult.Cleanup.Survived)
	require.NotContains(t, string(browserResult.Stdout), harness.bearer)
	var event struct {
		Event string `json:"event"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(browserResult.Stdout))), &event))
	require.Equal(t, "read_only_backend_complete", event.Event)
	approved := harness.GetGrantRequest(browserRequest.ID).Resource
	require.True(t, approved.ApprovedPolicy.ReadOnly)
	require.False(t, approved.RequestedPolicy.ReadOnly)
	grants := harness.ListGrants(principal.Resource.ID, catalog.ServerID)
	require.Len(t, grants, 2)
	for _, grant := range grants {
		require.True(t, grant.ReadOnly)
		readback := runOnlineCLI(t, harness, bearerPath, true, "grant", "get", grant.ID, "--json")
		var loaded contract.Grant
		require.NoError(t, json.Unmarshal(readback.Stdout, &loaded))
		require.True(t, loaded.ReadOnly)
	}
	readback := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "get", browserRequest.ID)
	assert.Contains(t, string(readback.Stdout), "unrestricted server tools")
	assert.Contains(t, string(readback.Stdout), "read-only server tools")
	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	for _, name := range []string{"write", "missing"} {
		callID := json.RawMessage(`"blocked"`)
		assertCallRejected(t, harness.ModernCall(issued.Bearer, callID, catalog.Namespace+"."+name, json.RawMessage(`{}`)), callID, contract.RejectionBlock, false)
	}
	require.Zero(t, catalog.CallCount())
	allowed := harness.ModernCall(issued.Bearer, json.RawMessage(`"read"`), catalog.Namespace+".read", json.RawMessage(`{}`))
	require.Contains(t, string(allowed.Body), `"result"`)
	require.Equal(t, 1, catalog.CallCount())
	harness.Stop(os.Interrupt)
}
