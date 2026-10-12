//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestE2EInvocationValidationEvidence(t *testing.T) {
	const canary = "PRIVATE-VALIDATION-CANARY"
	executable, err := os.Executable()
	require.NoError(t, err)
	temp := t.TempDir()
	events := filepath.Join(temp, "events")
	harness := newGatewayHarness(t)
	harness.Start()
	creation := createStdioServer(t, harness, executable, "legacy-call-validation", filepath.Join(temp, "marker"), events)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	harness.WaitSettledOperation(creation.Server.ID, creation.Operation.ID)
	waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveToolCount == 2
	})
	principal := harness.CreatePrincipal("Validation caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: creation.Server.ID, UpstreamName: pointerTo("alpha")})
	call := harness.ModernCall(issued.Bearer, json.RawMessage(`"validation"`), "stdio-legacy-call-validation.alpha", json.RawMessage(`{}`))
	id := assertCallError(t, call, json.RawMessage(`"validation"`), contract.DownstreamFailure, false)
	require.NotContains(t, string(call.Body), canary)
	var live struct {
		Error struct {
			Data contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(call.Body, &live))
	expected := &contract.FailureDiagnostics{
		GatewayObserved: contract.FailureObservation{Source: "tool", Reason: "reported_error"},
		ServerReported: &contract.ServerFailureDiagnostic{Version: 2, Category: "response_contract", Phase: "response_validation",
			Validation: &contract.ValidationDetails{Schema: "models_result", Version: 1, Truncated: true, Violations: []contract.ValidationViolation{
				{Code: "missing", Path: "$.models.[].name", Rule: "required"},
				{Code: "type", Path: "$.models.[].name", Rule: "type", Expected: "string", Observed: "number"},
			}}},
	}
	require.Equal(t, expected, live.Error.Data.Diagnostics)
	harness.WaitForAuditObservations(1, 0)
	harness.Restart()
	// Security readiness does not imply the asynchronous history opener settled.
	waitTrafficStatus(t, harness, func(s *contract.TrafficStatus) bool { return s.Ready })
	item := harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/invocations/"+id, nil)
	require.Equal(t, http.StatusOK, item.StatusCode)
	var retained contract.Invocation
	require.NoError(t, json.Unmarshal(item.Body, &retained))
	require.Equal(t, expected, retained.Diagnostics)
	require.NotContains(t, string(item.Body), canary)
	bearerPath := filepath.Join(temp, "administrator")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer), 0600))
	cli := runOnlineCLI(t, harness, bearerPath, true, "mcp", "invocation", "get", id, "--output", "json")
	require.JSONEq(t, string(item.Body), string(cli.Stdout))
	human := runOnlineCLI(t, harness, bearerPath, true, "mcp", "invocation", "get", id)
	require.Contains(t, string(human.Stdout), "Response validation (unverified)")
	require.Contains(t, string(human.Stdout), "Additional validation violations omitted")
	require.NotContains(t, string(human.Stdout)+string(human.Stderr), canary)
	harness.Stop(os.Interrupt)
	for _, result := range harness.results {
		require.NotContains(t, string(result.Stdout)+string(result.Stderr), canary)
	}
	observed := waitForFixtureEvents(t, events, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "request", "tools/call") >= 1
	})
	require.Len(t, fixtureEvents(observed, "request", "tools/call"), 1, "restart and evidence reads must not replay the call")
}
