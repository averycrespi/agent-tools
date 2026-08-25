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

type replacementMutation struct {
	Server struct {
		ID              string                 `json:"id"`
		DisplayName     string                 `json:"display_name"`
		DesiredRevision string                 `json:"desired_revision"`
		Runtime         contract.ServerRuntime `json:"runtime"`
		Catalog         contract.ServerCatalog `json:"catalog"`
	} `json:"server"`
	Operation *contract.ServerOperation `json:"operation"`
}

func TestGatewayBinaryPreservesDisplayAndReplacesStdioOnReloadAndBehavior(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	markerPath := filepath.Join(directory, "marker")
	creation := createStdioServer(t, harness, executable, "gated-modern-v1", markerPath, eventsPath)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	initial := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool { return countFixtureEvents(events, "start", "") == 1 })
	initialPID := fixtureEvents(events, "start", "")[0].PID
	etag := contract.ServerETag(creation.Server.ID, "1")

	display, etag := patchServer(t, harness, creation.Server.ID, etag, `{"display_name":"Renamed fixture"}`)
	assert.Equal(t, "Renamed fixture", display.Server.DisplayName)
	assert.Equal(t, "2", display.Server.DesiredRevision)
	assert.Nil(t, display.Operation)
	assert.Equal(t, *initial.Runtime.RuntimeID, *display.Server.Runtime.RuntimeID)
	assert.Equal(t, *initial.Catalog.ActiveRevision, *display.Server.Catalog.ActiveRevision)
	assert.Equal(t, 1, countFixtureEvents(fixtureEventsNow(t, eventsPath), "start", ""))

	reload := createServerOperation(t, harness, creation.Server.ID, etag, string(contract.OperationReload), "stdio-reload")
	blocked := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 2 && countFixtureEvents(events, "blocked", "") == 1
	})
	starts := fixtureEvents(blocked, "start", "")
	assert.Equal(t, initialPID, starts[1].PriorPID)
	assert.False(t, starts[1].PriorAlive)
	assertOperationState(t, harness, creation.Server.ID, reload.ID, contract.OperationRunning)
	assertReplacementWithheld(t, harness, creation.Server.ID, initial.Catalog.DurableRevision)
	require.NoError(t, syscall.Kill(starts[1].PID, syscall.SIGUSR1))
	harness.WaitOperation(creation.Server.ID, reload.ID, contract.OperationSucceeded)
	reloaded := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	assert.NotEqual(t, *initial.Runtime.RuntimeID, *reloaded.Runtime.RuntimeID)
	assert.NotEqual(t, *initial.Catalog.ActiveRevision, *reloaded.Catalog.ActiveRevision)
	waitForProcessExit(t, initialPID)

	transport := map[string]any{
		"kind": "stdio", "executable": executable,
		"arguments":         []string{"-test.run=^TestE2EStdioFixtureProcess$", "--", "mcp", "gated-modern-v2", markerPath, eventsPath},
		"working_directory": t.TempDir(), "environment": map[string]string{stdioFixtureEnvironment: "1"}, "secret_environment": map[string]string{},
	}
	body, err := json.Marshal(map[string]any{"transport": transport})
	require.NoError(t, err)
	behavior, _ := patchServer(t, harness, creation.Server.ID, etag, string(body))
	require.NotNil(t, behavior.Operation)
	blocked = waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 3 && countFixtureEvents(events, "blocked", "") == 2
	})
	starts = fixtureEvents(blocked, "start", "")
	assert.Equal(t, starts[1].PID, starts[2].PriorPID)
	assert.False(t, starts[2].PriorAlive)
	assertOperationState(t, harness, creation.Server.ID, behavior.Operation.ID, contract.OperationRunning)
	assertReplacementWithheld(t, harness, creation.Server.ID, reloaded.Catalog.DurableRevision)
	require.NoError(t, syscall.Kill(starts[2].PID, syscall.SIGUSR1))
	harness.WaitOperation(creation.Server.ID, behavior.Operation.ID, contract.OperationSucceeded)
	replaced := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	assert.NotEqual(t, *reloaded.Runtime.RuntimeID, *replaced.Runtime.RuntimeID)
	assert.NotEqual(t, *reloaded.Catalog.ActiveRevision, *replaced.Catalog.ActiveRevision)
	waitForProcessExit(t, starts[1].PID)
}

func TestGatewayBinaryPreservesDisplayAndReplacesHTTPOnReloadAndProtocolChange(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	fixture := newRawHTTPFixture(t, "modern")
	creation, etag := createHTTPServer(t, harness, fixture.URL(), "modern")
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	initial := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	initialRequests := len(fixture.Events())

	display, etag := patchServer(t, harness, creation.Server.ID, etag, `{"display_name":"Renamed HTTP"}`)
	assert.Nil(t, display.Operation)
	assert.Equal(t, *initial.Runtime.RuntimeID, *display.Server.Runtime.RuntimeID)
	assert.Equal(t, initialRequests, len(fixture.Events()))

	barrier := fixture.Arm("server/discover")
	reload := createServerOperation(t, harness, creation.Server.ID, etag, string(contract.OperationReload), "http-reload")
	awaitFixtureSignal(t, barrier.entered, "replacement discovery did not enter")
	assertOperationState(t, harness, creation.Server.ID, reload.ID, contract.OperationRunning)
	assertReplacementWithheld(t, harness, creation.Server.ID, initial.Catalog.DurableRevision)
	barrier.Release()
	harness.WaitOperation(creation.Server.ID, reload.ID, contract.OperationSucceeded)
	reloaded := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	assert.NotEqual(t, *initial.Runtime.RuntimeID, *reloaded.Runtime.RuntimeID)
	assert.NotEqual(t, *initial.Catalog.ActiveRevision, *reloaded.Catalog.ActiveRevision)

	fixture.SetMode("auto")
	barrier = fixture.Arm("server/discover")
	transport := map[string]any{"kind": "streamable_http", "url": fixture.URL(), "protocol_mode": "auto", "authentication": map[string]string{"mode": "none"}}
	body, err := json.Marshal(map[string]any{"transport": transport})
	require.NoError(t, err)
	behavior, _ := patchServer(t, harness, creation.Server.ID, etag, string(body))
	require.NotNil(t, behavior.Operation)
	awaitFixtureSignal(t, barrier.entered, "behavioral replacement discovery did not enter")
	assertOperationState(t, harness, creation.Server.ID, behavior.Operation.ID, contract.OperationRunning)
	assertReplacementWithheld(t, harness, creation.Server.ID, reloaded.Catalog.DurableRevision)
	barrier.Release()
	harness.WaitOperation(creation.Server.ID, behavior.Operation.ID, contract.OperationSucceeded)
	replaced := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	assert.NotEqual(t, *reloaded.Runtime.RuntimeID, *replaced.Runtime.RuntimeID)
	assert.NotEqual(t, *reloaded.Catalog.ActiveRevision, *replaced.Catalog.ActiveRevision)
	events := fixture.Events()
	require.GreaterOrEqual(t, len(events), initialRequests+8)
	assert.Equal(t, []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/list"}, fixtureMethods(events[len(events)-5:]))
}

func patchServer(t *testing.T, harness *gatewayHarness, serverID, etag, body string) (replacementMutation, string) {
	t.Helper()
	var mutation replacementMutation
	response := harness.AdminJSON(http.MethodPatch, "/api/v1/servers/"+serverID, body, map[string]string{"If-Match": etag}, &mutation)
	require.Equal(t, http.StatusOK, response.StatusCode)
	nextETag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	require.NotEmpty(t, nextETag)
	return mutation, nextETag
}

func activeCatalog(server stdioServerView) bool {
	return server.Runtime.State == contract.RuntimeActive && server.Runtime.RuntimeID != nil && server.Catalog.ActiveState == contract.ActiveCatalogCurrent && server.Catalog.ActiveRevision != nil
}

func assertOperationState(t *testing.T, harness *gatewayHarness, serverID, operationID string, state contract.ServerOperationState) {
	t.Helper()
	var operation contract.ServerOperation
	response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+serverID+"/operations/"+operationID, "", nil, &operation)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, state, operation.State)
}

func assertReplacementWithheld(t *testing.T, harness *gatewayHarness, serverID string, durableRevision *string) {
	t.Helper()
	server := waitForStdioServer(t, harness, serverID, func(server stdioServerView) bool {
		return (server.Runtime.State == contract.RuntimeStopping || server.Runtime.State == contract.RuntimeActivating) && server.Catalog.ActiveRevision == nil
	})
	require.NotNil(t, server.Catalog.DurableRevision)
	assert.Equal(t, *durableRevision, *server.Catalog.DurableRevision)
	assert.Equal(t, int64(0), server.Catalog.ActiveToolCount)
}

func fixtureEventsNow(t *testing.T, path string) []stdioFixtureEvent {
	t.Helper()
	return waitForFixtureEvents(t, path, func([]stdioFixtureEvent) bool { return true })
}

func fixtureMethods(events []httpFixtureEvent) []string {
	methods := make([]string, len(events))
	for index, event := range events {
		methods[index] = event.Method
	}
	return methods
}
