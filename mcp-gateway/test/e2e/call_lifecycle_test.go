//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryCallOccupancyDrainsAndNeverReplaysAfterRestart(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("call-capacity", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Capacity caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})

	barrier := catalog.Fixture.Arm("tools/call")
	completed := make(chan responseSnapshot, 1)
	go func() {
		completed <- harness.ModernCall(issued.Bearer, json.RawMessage(`"occupied"`), "call-capacity.alpha", json.RawMessage(`{}`))
	}()
	awaitFixtureSignal(t, barrier.entered, "call did not enter its downstream barrier")
	var status contract.SystemStatus
	statusResponse := harness.adminSnapshot(http.MethodGet, "/api/v1/system-status", nil)
	decodeSnapshot(t, statusResponse, http.StatusOK, &status)
	assert.Equal(t, int64(32), status.Limits.DownstreamDispatch.Limit)
	assert.Equal(t, int64(1), status.Limits.DownstreamDispatch.InUse)
	assert.Equal(t, contract.LimitStatus{InUse: 1, Limit: 4}, currentServer(t, harness, catalog.ServerID).Runtime.Dispatch)
	barrier.Release()
	assertCallSuccess(t, <-completed)

	harness.Stop(syscall.SIGTERM)
	harness.Start()
	waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
	assert.Equal(t, 1, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"), "restart replayed an admitted call")
	fresh := harness.ModernCall(issued.Bearer, json.RawMessage(`"fresh-after-restart"`), "call-capacity.alpha", json.RawMessage(`{}`))
	assertCallSuccess(t, fresh)
	assert.Equal(t, 2, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))

	harness.Stop(syscall.SIGTERM)
	observations := harness.AuditObservations()
	require.Len(t, observations, 2)
	for _, observation := range observations {
		assert.Equal(t, contract.TerminalSucceeded, observation.TerminalClass)
	}
}

func TestGatewayBinaryClassifiesCompleteLossCancellationAndReplacementOnce(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("call-certainty", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Certainty caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})

	cancelledContext, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	beforeRequest, err := http.NewRequestWithContext(cancelledContext, http.MethodPost, "http://"+harness.authority+"/mcp", bytes.NewReader(rawRPCBody(t, json.RawMessage(`"cancel-before"`), "tools/call", callParams(t, "call-certainty.alpha", json.RawMessage(`{}`), `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`))))
	require.NoError(t, err)
	beforeRequest.Header.Set("Authorization", issued.Bearer.authorizationHeader())
	beforeRequest.Header.Set("Content-Type", contract.MediaTypeJSON)
	beforeRequest.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	_, err = harness.client.Do(beforeRequest)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, harness.LiveAuditObservations())
	assert.Zero(t, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))

	catalog.Fixture.SetCallOutcome(fixtureCallMalformed)
	malformed := harness.ModernCall(issued.Bearer, json.RawMessage(`"malformed"`), "call-certainty.alpha", json.RawMessage(`{}`))
	assertCallError(t, malformed, json.RawMessage(`"malformed"`), contract.DownstreamFailure, false)
	catalog.Fixture.SetCallOutcome(fixtureCallUncertain)
	lost := harness.ModernCall(issued.Bearer, json.RawMessage(`"transport-loss"`), "call-certainty.alpha", json.RawMessage(`{}`))
	assertCallError(t, lost, json.RawMessage(`"transport-loss"`), contract.OutcomeUnknown, true)

	catalog.Fixture.SetCallOutcome(fixtureCallSuccess)
	cancelBarrier := catalog.Fixture.Arm("tools/call")
	requestContext, cancelAfter := context.WithCancel(context.Background())
	afterRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://"+harness.authority+"/mcp", bytes.NewReader(rawRPCBody(t, json.RawMessage(`"cancel-after"`), "tools/call", callParams(t, "call-certainty.alpha", json.RawMessage(`{}`), `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`))))
	require.NoError(t, err)
	afterRequest.Header.Set("Authorization", issued.Bearer.authorizationHeader())
	afterRequest.Header.Set("Content-Type", contract.MediaTypeJSON)
	afterRequest.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := harness.client.Do(afterRequest)
		if response != nil {
			requestErr = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	awaitFixtureSignal(t, cancelBarrier.entered, "cancelled call did not reach downstream")
	cancelAfter()
	require.ErrorIs(t, <-requestDone, context.Canceled)
	awaitFixtureSignal(t, cancelBarrier.cancelled, "downstream did not observe caller cancellation")
	awaitFixtureSignal(t, cancelBarrier.completed, "cancelled downstream handler did not complete")
	require.Eventually(t, func() bool {
		return len(harness.LiveAuditObservations()) == 3
	}, 3*time.Second, 10*time.Millisecond)

	harness.Stop(syscall.SIGTERM)
	observations := harness.AuditObservations()
	require.Len(t, observations, 3)
	assert.Equal(t, contract.TerminalDownstreamFailure, observations[0].TerminalClass)
	assert.Equal(t, contract.TerminalOutcomeUnknown, observations[1].TerminalClass)
	assert.Contains(t, []contract.InvocationTerminalClass{"", contract.TerminalOutcomeUnknown}, observations[2].TerminalClass)
	assert.Equal(t, 3, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))
}

func TestGatewayBinaryReplacementWithdrawsPinnedCallWithoutReroute(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	catalog := harness.SetupCurrentCatalog("call-replacement", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	principal := harness.CreatePrincipal("Replacement caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
	barrier := catalog.Fixture.Arm("tools/call")
	callDone := make(chan responseSnapshot, 1)
	go func() {
		callDone <- harness.ModernCall(issued.Bearer, json.RawMessage(`"replacement"`), "call-replacement.alpha", json.RawMessage(`{}`))
	}()
	awaitFixtureSignal(t, barrier.entered, "replacement call did not reach downstream")
	transport := fmt.Sprintf(`{"transport":{"kind":"streamable_http","url":%q,"protocol_mode":"auto","authentication":{"mode":"none"}}}`, catalog.Fixture.URL())
	mutation, _ := patchServer(t, harness, catalog.ServerID, catalog.ETag, transport)
	require.NotNil(t, mutation.Operation)
	awaitFixtureSignal(t, barrier.cancelled, "replacement did not cancel the pinned old route")
	barrier.Release()
	awaitFixtureSignal(t, barrier.completed, "replacement call handler did not complete")
	response := <-callDone
	assertCallError(t, response, json.RawMessage(`"replacement"`), contract.OutcomeUnknown, true)
	assert.Equal(t, 1, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"), "replacement rerouted the in-flight call")
	assertOperationState(t, harness, catalog.ServerID, mutation.Operation.ID, contract.OperationRunning)

	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, harness.authority)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	result, waitErr := harness.process.Wait()
	harness.process = nil
	require.Error(t, waitErr)
	assert.Equal(t, 2, result.ExitCode)
	_, markerErr := os.Stat(filepath.Join(harness.root, "run.unclean"))
	require.NoError(t, markerErr)
	observations := harness.AuditObservations()
	require.Len(t, observations, 1)
	assert.Equal(t, contract.DecisionAllow, observations[0].Decision)
	// Withdrawal evidence shares the nonqueueing writer with best-effort terminal annotation.
	assert.Contains(t, []contract.InvocationTerminalClass{"", contract.TerminalOutcomeUnknown}, observations[0].TerminalClass)
	assert.Equal(t, 1, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))
}

func TestGatewayBinaryClassifiesLegacySessionAndStdioProcessLossWithoutReplay(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	principal := harness.CreatePrincipal("Loss caller", contract.VisibilityAll)
	issued := harness.IssueCredential(principal)

	httpFixture := newRawHTTPFixture(t, "auto")
	httpCreation, _ := createHTTPServer(t, harness, httpFixture.URL(), "auto")
	harness.WaitOperation(httpCreation.Server.ID, httpCreation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, httpCreation.Server.ID, activeCatalog)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: httpCreation.Server.ID, UpstreamName: pointerTo("http-alpha")})
	httpFixture.LoseSession()
	sessionLoss := harness.ModernCall(issued.Bearer, json.RawMessage(`"session-loss"`), "http-auto.http-alpha", json.RawMessage(`{}`))
	sessionInvocationID := assertCallError(t, sessionLoss, json.RawMessage(`"session-loss"`), contract.OutcomeUnknown, true)
	assert.Equal(t, 1, httpFixtureMethodCount(httpFixture.Events(), "tools/call"))

	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	stdioCreation := createStdioServer(t, harness, executable, "legacy-call-uncertain", filepath.Join(directory, "marker"), eventsPath)
	harness.WaitOperation(stdioCreation.Server.ID, stdioCreation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, stdioCreation.Server.ID, activeCatalog)
	harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: stdioCreation.Server.ID, UpstreamName: pointerTo("alpha")})
	processLoss := harness.ModernCall(issued.Bearer, json.RawMessage(`"process-loss"`), "stdio-legacy-call-uncertain.alpha", json.RawMessage(`{}`))
	processInvocationID := assertCallError(t, processLoss, json.RawMessage(`"process-loss"`), contract.OutcomeUnknown, true)
	events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "request", "tools/call") == 1
	})
	assert.Equal(t, 1, countFixtureEvents(events, "request", "tools/call"))

	harness.Stop(syscall.SIGTERM)
	observations := harness.AuditObservations()
	require.Len(t, observations, 2)
	assert.Equal(t, sessionInvocationID, observations[0].InvocationID)
	assert.Equal(t, processInvocationID, observations[1].InvocationID)
	for _, observation := range observations {
		assert.Equal(t, contract.DecisionAllow, observation.Decision)
		// Runtime withdrawal can contend with the best-effort terminal writer.
		assert.Contains(t, []contract.InvocationTerminalClass{"", contract.TerminalOutcomeUnknown}, observation.TerminalClass)
	}
	assert.Equal(t, 1, httpFixtureMethodCount(httpFixture.Events(), "tools/call"))
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, eventsPath), "request", "tools/call"))
}

func assertCallSuccess(t *testing.T, response responseSnapshot) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	var envelope struct {
		Result struct {
			Content []json.RawMessage `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	require.Len(t, envelope.Result.Content, 1)
	assert.JSONEq(t, `{"type":"text","text":"fixture success"}`, string(envelope.Result.Content[0]))
}

func assertCallError(t *testing.T, response responseSnapshot, expectedID json.RawMessage, code contract.AgentCallErrorCode, outcomeUnknown bool) string {
	t.Helper()
	require.Equal(t, http.StatusOK, response.StatusCode, string(response.Body))
	callError, ok := contract.AgentCallErrorForCode(code)
	require.True(t, ok)
	var envelope struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int                         `json:"code"`
			Message string                      `json:"message"`
			Data    contract.AgentCallErrorData `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body, &envelope))
	require.NotNil(t, envelope.Error.Data.InvocationID)
	invocationID := *envelope.Error.Data.InvocationID
	expectedUnknown := ""
	if outcomeUnknown {
		expectedUnknown = `,"outcomeUnknown":true`
	}
	expected := `{"jsonrpc":"2.0","id":` + string(expectedID) + `,"error":{"code":-32000,"message":` + strconv.Quote(callError.Message) + `,"data":{"code":` + strconv.Quote(string(code)) + `,"invocationId":` + strconv.Quote(invocationID) + expectedUnknown + `}}}`
	assert.Equal(t, expected, string(response.Body))
	assert.JSONEq(t, string(expectedID), string(envelope.ID))
	assert.Equal(t, contract.AgentCallJSONRPCErrorCode, envelope.Error.Code)
	assert.Equal(t, callError.Message, envelope.Error.Message)
	assert.Equal(t, code, envelope.Error.Data.Code)
	assert.Equal(t, outcomeUnknown, envelope.Error.Data.OutcomeUnknown)
	return invocationID
}
