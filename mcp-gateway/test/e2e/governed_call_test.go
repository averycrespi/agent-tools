//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryGovernsModernAndLegacyCallsBeforeHTTPDispatch(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	strictSchema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	catalog := harness.SetupCurrentCatalog("governed-http", []fixtureTool{
		{Name: "allowed", InputSchema: strictSchema},
		{Name: "denied", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "blocked", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	principal := harness.CreatePrincipal("Governed HTTP caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	allow := harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("allowed")})
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantDeny, ServerID: catalog.ServerID, UpstreamName: pointerTo("denied")})

	beforeDiscovery := harness.ModernList(issued.Bearer, json.RawMessage(`"before-calls"`), "")
	assert.Equal(t, withSyntheticNames([]string{"governed-http.allowed", "governed-http.blocked", "governed-http.denied"}), discoveryToolNames(t, beforeDiscovery))
	session, _ := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
	legacySuccess := harness.LegacyCall(issued.Bearer, session, json.RawMessage(`"legacy-allow"`), "governed-http.allowed", json.RawMessage(`{"value":"legacy"}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"legacy-allow","result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(legacySuccess.Body))

	assertCallRejected(t, harness.ModernCall(issued.Bearer, json.RawMessage(`"deny"`), "governed-http.denied", json.RawMessage(`{}`)), json.RawMessage(`"deny"`))
	assertCallRejected(t, harness.LegacyCall(issued.Bearer, session, json.RawMessage(`"block"`), "governed-http.blocked", json.RawMessage(`{}`)), json.RawMessage(`"block"`))
	assertCallRejected(t, harness.ModernCall(issued.Bearer, json.RawMessage(`"invalid"`), "governed-http.allowed", json.RawMessage(`{"value":7}`)), json.RawMessage(`"invalid"`))
	assertCallRejected(t, harness.LegacyCall(issued.Bearer, session, json.RawMessage(`"unknown"`), "governed-http.absent", json.RawMessage(`{}`)), json.RawMessage(`"unknown"`))
	afterMatrixDiscovery := harness.ModernList(issued.Bearer, json.RawMessage(`"after-calls"`), "")
	assert.Equal(t, discoveryToolNames(t, beforeDiscovery), discoveryToolNames(t, afterMatrixDiscovery))

	barrier := catalog.Fixture.Arm("tools/call")
	callDone := make(chan responseSnapshot, 1)
	go func() {
		callDone <- harness.ModernCall(issued.Bearer, json.RawMessage(`"modern-allow"`), "governed-http.allowed", json.RawMessage(`{"value":"modern"}`))
	}()
	awaitFixtureSignal(t, barrier.entered, "committed ALLOW did not reach downstream barrier")
	live := harness.LiveAuditObservations()
	require.Len(t, live, 6)
	assert.Equal(t, contract.DecisionAllow, live[5].Decision)
	assert.Empty(t, live[5].TerminalClass, "terminal annotation preceded downstream completion")
	assert.Equal(t, 2, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))

	harness.DeleteGrant(allow.ID)
	harness.RevokeCredential(issued.Principal)
	barrier.Release()
	modernSuccess := <-callDone
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"modern-allow","result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(modernSuccess.Body))
	unauthenticated := harness.ModernCall(issued.Bearer, json.RawMessage(`"after-revoke"`), "governed-http.allowed", json.RawMessage(`{"value":"later"}`))
	assertProblem(t, unauthenticated, http.StatusUnauthorized, "authentication_required", "Authentication is required.", true)
	assert.Equal(t, 2, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"), "rejected calls must not dispatch")

	harness.Stop(syscall.SIGTERM)
	observations := harness.AuditObservations()
	require.Len(t, observations, 6)
	assert.Equal(t, contract.DecisionAllow, observations[0].Decision)
	assert.Equal(t, contract.TerminalSucceeded, observations[0].TerminalClass)
	assert.Equal(t, contract.DecisionDeny, observations[1].Decision)
	assert.Equal(t, contract.DecisionBlock, observations[2].Decision)
	assert.Equal(t, contract.AdmissionInvalidArguments, observations[3].AdmissionClass)
	assert.Equal(t, contract.AdmissionUnknownTool, observations[4].AdmissionClass)
	assert.Equal(t, contract.DecisionAllow, observations[5].Decision)
	assert.Equal(t, contract.TerminalSucceeded, observations[5].TerminalClass)
}

func TestGatewayBinaryRoutesBothInboundErasToLegacyStdioOnce(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	creation := createStdioServer(t, harness, executable, "legacy-call-success", filepath.Join(directory, "marker"), eventsPath)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	principal := harness.CreatePrincipal("Legacy stdio caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: creation.Server.ID, UpstreamName: pointerTo("alpha")})

	modern := harness.ModernCall(issued.Bearer, json.RawMessage(`1`), "stdio-legacy-call-success.alpha", json.RawMessage(`{}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(modern.Body))
	session, _ := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`2`))
	legacy := harness.LegacyCall(issued.Bearer, session, json.RawMessage(`3`), "stdio-legacy-call-success.alpha", json.RawMessage(`{}`))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"fixture success"}]}}`, string(legacy.Body))
	events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "request", "tools/call") == 2
	})
	assert.Equal(t, 2, countFixtureEvents(events, "request", "tools/call"))
	starts := fixtureEvents(events, "start", "")
	require.NotEmpty(t, starts)
	activePID := starts[len(starts)-1].PID

	harness.Stop(syscall.SIGTERM)
	waitForProcessExit(t, activePID)
	observations := harness.AuditObservations()
	require.Len(t, observations, 2)
	for _, observation := range observations {
		assert.Equal(t, contract.DecisionAllow, observation.Decision)
		assert.Equal(t, contract.TerminalSucceeded, observation.TerminalClass)
	}
}

func discoveryToolNames(t *testing.T, response responseSnapshot) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	names := make([]string, len(envelope.Result.Tools))
	for index, tool := range envelope.Result.Tools {
		names[index] = tool.Name
	}
	return names
}

func httpFixtureMethodCount(events []httpFixtureEvent, method string) int {
	count := 0
	for _, event := range events {
		if event.Method == method {
			count++
		}
	}
	return count
}
