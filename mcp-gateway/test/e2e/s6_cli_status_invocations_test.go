//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIStatusInvocations(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))

	statusJSON := runOnlineCLI(t, harness, bearerPath, true, "status", "--output", "json")
	statusAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	assert.JSONEq(t, string(statusAPI.Body), string(statusJSON.Stdout), "JSON mode must preserve the exact API projection")
	var status contract.SystemStatus
	require.NoError(t, json.Unmarshal(statusJSON.Stdout, &status))
	assert.Equal(t, contract.ProcessReady, status.Process.State)
	assert.True(t, status.Process.Ready)
	assert.Equal(t, contract.SQLiteReady, status.SQLite.State)
	assert.Empty(t, statusJSON.Stderr)

	statusTable := runOnlineCLI(t, harness, bearerPath, true, "status")
	assert.Contains(t, string(statusTable.Stdout), "process")
	assert.Contains(t, string(statusTable.Stdout), "ready")
	assert.Contains(t, string(statusTable.Stdout), "sqlite")
	assert.NotContains(t, string(statusTable.Stdout), harness.bearer)

	captureCanary := "CLI_CAPTURE_" + strings.Repeat("c", 32)
	catalog := harness.SetupCurrentCatalog("cli-invocations", []fixtureTool{{Name: "allowed", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("CLI reader", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("allowed")})
	barrier := catalog.Fixture.Arm("tools/call")
	callDone := make(chan responseSnapshot, 1)
	go func() {
		callDone <- harness.ModernCall(issued.Bearer, json.RawMessage(`"cli-held"`), "cli-invocations.allowed", json.RawMessage(`{"note":"`+captureCanary+`"}`))
	}()
	awaitFixtureSignal(t, barrier.entered, "CLI fixture call did not reach the downstream barrier")

	listJSON := runOnlineCLI(t, harness, bearerPath, true,
		"invocation", "list", "--output", "json", "--limit", "1", "--requested-name", "cli-invocations.allowed",
	)
	listAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations?limit=1&requested_name=cli-invocations.allowed", nil)
	assert.JSONEq(t, string(listAPI.Body), string(listJSON.Stdout), "JSON list mode must preserve the exact API projection")
	var page contract.InvocationPage
	require.NoError(t, json.Unmarshal(listJSON.Stdout, &page))
	require.Len(t, page.Items, 1)
	item := page.Items[0]
	assert.Equal(t, contract.InvocationOutcomeUnknown, item.Outcome.Class)
	assert.Equal(t, contract.InvocationBasisMissingTerminal, item.Outcome.Basis)
	assert.NotContains(t, string(listJSON.Stdout), "redacted_arguments")
	assert.NotContains(t, string(listJSON.Stdout), captureCanary)

	listTable := runOnlineCLI(t, harness, bearerPath, true,
		"invocation", "list", "--limit", "1", "--requested-name", "cli-invocations.allowed",
	)
	assert.Contains(t, string(listTable.Stdout), "outcome_unknown")
	assert.Contains(t, string(listTable.Stdout), "missing_terminal")
	assert.Contains(t, string(listTable.Stdout), "does not automatically replay")
	assert.Contains(t, string(listTable.Stdout), "may duplicate an effect")
	assert.NotContains(t, string(listTable.Stdout), captureCanary)
	assert.NotContains(t, string(listTable.Stdout), "redacted_arguments")

	getJSON := runOnlineCLI(t, harness, bearerPath, true, "invocation", "get", item.ID, "--output", "json")
	getAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/invocations/"+item.ID, nil)
	assert.JSONEq(t, string(getAPI.Body), string(getJSON.Stdout), "JSON item mode must preserve the exact API projection")
	var invocation contract.Invocation
	require.NoError(t, json.Unmarshal(getJSON.Stdout, &invocation))
	assert.Equal(t, item.ID, invocation.ID)
	assert.Contains(t, string(invocation.RedactedArguments), captureCanary)
	getTable := runOnlineCLI(t, harness, bearerPath, true, "invocation", "get", item.ID)
	assert.Contains(t, string(getTable.Stdout), item.ID)
	assert.NotContains(t, string(getTable.Stdout), captureCanary)
	assert.NotContains(t, string(getTable.Stdout), "redacted_arguments")

	local := harness.ModernSelfServiceCall(issued.Bearer, json.RawMessage(`"local"`), "get_identity", map[string]any{})
	require.Equal(t, http.StatusOK, local.StatusCode, string(local.Body))
	localTable := runOnlineCLI(t, harness, bearerPath, true,
		"invocation", "list", "--limit", "1", "--server-id", contract.SyntheticServerID,
	)
	assert.Contains(t, string(localTable.Stdout), "gateway:get_identity")
	assert.NotContains(t, string(localTable.Stdout), "downstream handoff")

	barrier.Release()
	completed := <-callDone
	require.Equal(t, http.StatusOK, completed.StatusCode, string(completed.Body))
	harness.Stop(syscall.SIGTERM)

	failure := runOnlineCLI(t, harness, bearerPath, false, "invocation", "get", item.ID, "--output", "json")
	assert.Equal(t, 9, failure.ExitCode)
	assert.Empty(t, failure.Stdout)
	assert.Contains(t, string(failure.Stderr), `"code":"client_transport_failure"`)
	assert.Contains(t, string(failure.Stderr), "safe to repeat")
	for _, result := range []testutil.ProcessResult{statusJSON, statusTable, listJSON, listTable, getJSON, getTable, localTable, failure} {
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T19 must own one Gateway lifecycle")
}

func runOnlineCLI(t *testing.T, harness *gatewayHarness, bearerPath string, success bool, args ...string) testutil.ProcessResult {
	t.Helper()
	args = append(args,
		"--address", "http://"+harness.authority,
		"--admin-bearer-file", bearerPath,
	)
	result, err := harness.runner.Run(context.Background(), harness.binary, args...)
	if success {
		require.NoError(t, err, "%v: %s", args, result.Stderr)
		assert.Equal(t, 0, result.ExitCode)
	} else {
		require.Error(t, err)
	}
	return result
}
