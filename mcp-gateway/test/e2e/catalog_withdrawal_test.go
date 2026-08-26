//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryHidesDurableStaleAndWithdrawnCatalogs(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	names := discoveryNames("withdrawal", 98)
	catalog := harness.SetupCurrentCatalog("withdrawal", fixtureTools(names))
	principal := harness.CreatePrincipal("Catalog observer", contract.VisibilityAll)
	credential := harness.IssueCredential(principal)
	initialServer := currentServer(t, harness, catalog.ServerID)
	require.NotNil(t, initialServer.Runtime.RuntimeID)
	require.NotNil(t, initialServer.Catalog.DurableRevision)
	assert.Equal(t, contract.DurableCatalogCurrent, initialServer.Catalog.DurableState)
	assert.Equal(t, contract.ActiveCatalogCurrent, initialServer.Catalog.ActiveState)
	assert.Equal(t, int64(101), initialServer.Catalog.ActiveToolCount)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 4}, initialServer.Runtime.Dispatch)

	initialAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"initial-agent"`), "")
	initialAgentCursor := discoveryCursor(t, initialAgent)
	assertDiscoveryPage(t, initialAgent, json.RawMessage(`"initial-agent"`), names[:100], initialAgentCursor)
	initialControl := controlCatalog(t, harness, "/api/v1/catalog?limit=1")
	require.Len(t, initialControl.Items, 1)
	require.NotNil(t, initialControl.NextCursor)
	initialControlCursor := *initialControl.NextCursor
	descriptor := initialControl.Items[0]
	assertDescriptorReadable(t, harness, catalog.ServerID, descriptor)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 1, "tools/list": 2})

	call := harness.ModernRequest(credential.Bearer, []byte(`{"jsonrpc":"2.0","id":"no-http-call","method":"tools/call","params":{"name":"withdrawal.tool-000","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
	assertRPCError(t, call, `{"jsonrpc":"2.0","id":"no-http-call","error":{"code":-32601,"message":"Method not found."}}`)
	assert.Equal(t, contract.MediaTypeJSON, call.Header.Get("Content-Type"))
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 1, "tools/list": 2})
	afterCall := currentServer(t, harness, catalog.ServerID)
	assert.Equal(t, initialServer.Runtime.RuntimeID, afterCall.Runtime.RuntimeID)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 4}, afterCall.Runtime.Dispatch)

	harness.Stop(syscall.SIGTERM)
	reconstruction := catalog.Fixture.Arm("server/discover")
	t.Cleanup(reconstruction.Release)
	harness.Start()
	awaitFixtureSignal(t, reconstruction.entered, "reconstruction did not reach HTTP fixture")
	blocked := currentServer(t, harness, catalog.ServerID)
	assert.Equal(t, contract.DurableCatalogCurrent, blocked.Catalog.DurableState)
	assert.Equal(t, initialServer.Catalog.DurableRevision, blocked.Catalog.DurableRevision)
	assert.Equal(t, int64(101), blocked.Catalog.DurableToolCount)
	assert.Equal(t, contract.ActiveCatalogAbsent, blocked.Catalog.ActiveState)
	assert.Nil(t, blocked.Catalog.ActiveRevision)
	assert.Zero(t, blocked.Catalog.ActiveToolCount)

	durableOnlyControl := controlCatalog(t, harness, "/api/v1/catalog?limit=100")
	assert.Equal(t, contract.AggregateCatalogEmpty, durableOnlyControl.Catalog.ActiveState)
	assert.Empty(t, durableOnlyControl.Items)
	assertDescriptorReadable(t, harness, catalog.ServerID, descriptor)
	durableOnlyAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"durable-only"`), "")
	assertDiscoveryPage(t, durableOnlyAgent, json.RawMessage(`"durable-only"`), nil, "")
	staleInitialControl := harness.adminSnapshot(http.MethodGet, "/api/v1/catalog?cursor="+url.QueryEscape(initialControlCursor), nil)
	assertProblem(t, staleInitialControl, http.StatusConflict, "stale_cursor", "The cursor snapshot is no longer available.", false)
	staleInitialAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"restart-cursor"`), initialAgentCursor)
	assertRPCError(t, staleInitialAgent, `{"jsonrpc":"2.0","id":"restart-cursor","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 2, "tools/list": 2})

	reconstruction.Release()
	awaitFixtureSignal(t, reconstruction.completed, "reconstruction did not leave HTTP fixture")
	reconstructed := waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
	require.NotNil(t, reconstructed.Runtime.RuntimeID)
	assert.NotEqual(t, initialServer.Runtime.RuntimeID, reconstructed.Runtime.RuntimeID)
	assert.Equal(t, int64(101), reconstructed.Catalog.ActiveToolCount)
	postRestartControl := controlCatalog(t, harness, "/api/v1/catalog?limit=1")
	require.NotNil(t, postRestartControl.NextCursor)
	postRestartAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"post-restart"`), "")
	postRestartAgentCursor := discoveryCursor(t, postRestartAgent)
	assertDiscoveryPage(t, postRestartAgent, json.RawMessage(`"post-restart"`), names[:100], postRestartAgentCursor)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 2, "tools/list": 4})

	catalog.Fixture.SetTools([]fixtureTool{
		{Name: "duplicate", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "duplicate", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	refresh := createServerOperation(t, harness, catalog.ServerID, catalog.ETag, string(contract.OperationRefreshCatalog), "t39-stale-refresh")
	failedRefresh := harness.WaitOperation(catalog.ServerID, refresh.ID, contract.OperationFailed)
	require.NotNil(t, failedRefresh.Reason)
	assert.Equal(t, contract.ReasonCatalogInvalid, *failedRefresh.Reason)
	stale := waitForStdioServer(t, harness, catalog.ServerID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogStale && server.Catalog.DurableState == contract.DurableCatalogStale
	})
	assert.Equal(t, reconstructed.Runtime.RuntimeID, stale.Runtime.RuntimeID)
	assert.Equal(t, int64(101), stale.Catalog.ActiveToolCount)
	assert.Equal(t, int64(101), stale.Catalog.DurableToolCount)
	staleControl := controlCatalog(t, harness, "/api/v1/catalog?limit=1")
	assert.Equal(t, contract.AggregateCatalogDegraded, staleControl.Catalog.ActiveState)
	assert.Positive(t, staleControl.Catalog.IssueCount)
	require.Len(t, staleControl.Items, 1)
	assertDescriptorReadable(t, harness, catalog.ServerID, descriptor)
	staleAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"stale-agent"`), "")
	assertDiscoveryPage(t, staleAgent, json.RawMessage(`"stale-agent"`), nil, "")
	stalePostRestartControl := harness.adminSnapshot(http.MethodGet, "/api/v1/catalog?cursor="+url.QueryEscape(*postRestartControl.NextCursor), nil)
	assertProblem(t, stalePostRestartControl, http.StatusConflict, "stale_cursor", "The cursor snapshot is no longer available.", false)
	stalePostRestartAgent := harness.ModernList(credential.Bearer, json.RawMessage(`"stale-agent-cursor"`), postRestartAgentCursor)
	assertRPCError(t, stalePostRestartAgent, `{"jsonrpc":"2.0","id":"stale-agent-cursor","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 2, "tools/list": 5})

	disabled, disabledETag := patchServer(t, harness, catalog.ServerID, catalog.ETag, `{"enabled":false}`)
	require.NotNil(t, disabled.Operation)
	harness.WaitOperation(catalog.ServerID, disabled.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, catalog.ServerID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeInactive && server.Catalog.ActiveState == contract.ActiveCatalogAbsent && server.Catalog.DurableState == contract.DurableCatalogStale
	})
	disabledServer := currentServer(t, harness, catalog.ServerID)
	assert.Equal(t, contract.DesiredServerDisabled, disabledServer.DesiredState)
	assert.Empty(t, controlCatalog(t, harness, "/api/v1/catalog?limit=100").Items)
	assertDiscoveryPage(t, harness.ModernList(credential.Bearer, json.RawMessage(`"disabled-agent"`), ""), json.RawMessage(`"disabled-agent"`), nil, "")
	assertDescriptorReadable(t, harness, catalog.ServerID, descriptor)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 2, "tools/list": 5})

	var deletion replacementMutation
	deleteResponse := harness.AdminJSON(http.MethodDelete, "/api/v1/servers/"+catalog.ServerID, `{}`, map[string]string{"If-Match": disabledETag}, &deletion)
	require.Equal(t, http.StatusAccepted, deleteResponse.StatusCode)
	require.NoError(t, deleteResponse.Body.Close())
	require.NotNil(t, deletion.Operation)
	harness.WaitOperation(catalog.ServerID, deletion.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, catalog.ServerID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeDeleted && server.Catalog.ActiveState == contract.ActiveCatalogAbsent && server.Catalog.DurableState == contract.DurableCatalogRetired
	})
	deleted := currentServer(t, harness, catalog.ServerID)
	assert.Equal(t, contract.DesiredServerDeleted, deleted.DesiredState)
	assert.Equal(t, "3", deleted.DesiredRevision)
	assert.Empty(t, controlCatalog(t, harness, "/api/v1/catalog?limit=100").Items)
	assertDiscoveryPage(t, harness.ModernList(credential.Bearer, json.RawMessage(`"deleted-agent"`), ""), json.RawMessage(`"deleted-agent"`), nil, "")
	assertDescriptorReadable(t, harness, catalog.ServerID, descriptor)
	assertHTTPMethods(t, catalog.Fixture, map[string]int{"server/discover": 2, "tools/list": 5})
}

func TestGatewayBinaryToolsCallHasNoStdioProcessEffect(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	executable, err := os.Executable()
	require.NoError(t, err)
	directory := t.TempDir()
	eventsPath := filepath.Join(directory, "events.jsonl")
	creation := createStdioServer(t, harness, executable, "modern", filepath.Join(directory, "marker"), eventsPath)
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	beforeServer := waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	require.NotNil(t, beforeServer.Runtime.RuntimeID)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 4}, beforeServer.Runtime.Dispatch)
	events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 1 && countFixtureEvents(events, "request", "tools/list") == 2
	})
	beforeRequests := len(fixtureEvents(events, "request", ""))
	start := fixtureEvents(events, "start", "")[0]
	require.True(t, processExists(start.PID))

	principal := harness.CreatePrincipal("No call", contract.VisibilityAll)
	credential := harness.IssueCredential(principal)
	call := harness.ModernRequest(credential.Bearer, []byte(`{"jsonrpc":"2.0","id":"no-stdio-call","method":"tools/call","params":{"name":"stdio-modern.alpha","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"e2e-harness","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
	assertRPCError(t, call, `{"jsonrpc":"2.0","id":"no-stdio-call","error":{"code":-32601,"message":"Method not found."}}`)

	afterEvents := fixtureEventsNow(t, eventsPath)
	assert.Equal(t, 1, countFixtureEvents(afterEvents, "start", ""))
	assert.Equal(t, beforeRequests, len(fixtureEvents(afterEvents, "request", "")))
	assert.Zero(t, countFixtureEvents(afterEvents, "request", "tools/call"))
	assert.True(t, processExists(start.PID))
	afterServer := currentServer(t, harness, creation.Server.ID)
	assert.Equal(t, beforeServer.Runtime.RuntimeID, afterServer.Runtime.RuntimeID)
	assert.Equal(t, contract.LimitStatus{InUse: 0, Limit: 4}, afterServer.Runtime.Dispatch)
}

func currentServer(t *testing.T, harness *gatewayHarness, serverID string) destructiveServerView {
	t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+serverID, nil)
	var server destructiveServerView
	decodeSnapshot(t, response, http.StatusOK, &server)
	return server
}

func controlCatalog(t *testing.T, harness *gatewayHarness, path string) contract.CatalogPage {
	t.Helper()
	response := harness.adminSnapshot(http.MethodGet, path, nil)
	var page contract.CatalogPage
	decodeSnapshot(t, response, http.StatusOK, &page)
	return page
}

func assertDescriptorReadable(t *testing.T, harness *gatewayHarness, serverID string, expected contract.ToolDescriptor) {
	t.Helper()
	response := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+serverID+"/descriptors/"+expected.ID, nil)
	var descriptor contract.ToolDescriptor
	decodeSnapshot(t, response, http.StatusOK, &descriptor)
	assert.Equal(t, expected.ID, descriptor.ID)
	assert.Equal(t, expected.ServerID, descriptor.ServerID)
	assert.Equal(t, expected.UpstreamName, descriptor.UpstreamName)
	assert.Equal(t, expected.ExternalName, descriptor.ExternalName)
	assert.Equal(t, expected.Descriptor, descriptor.Descriptor)
	assert.Equal(t, expected.Fingerprint, descriptor.Fingerprint)
	assert.Equal(t, expected.FirstSeenAt, descriptor.FirstSeenAt)
	assert.NotEmpty(t, descriptor.CatalogRevision)
	assert.NotEmpty(t, descriptor.LastSeenAt)
}

func assertHTTPMethods(t *testing.T, fixture *rawHTTPFixture, expected map[string]int) {
	t.Helper()
	actual := make(map[string]int)
	for _, event := range fixture.Events() {
		actual[event.Method]++
	}
	assert.Equal(t, expected, actual)
}
