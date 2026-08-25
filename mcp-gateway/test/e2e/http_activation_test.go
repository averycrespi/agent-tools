//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryActivatesAndPublishesHardenedHTTP(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "modern", mode: "modern"},
		{name: "fresh auto fallback to legacy", mode: "auto"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t)
			harness.Start()
			fixture := newRawHTTPFixture(t, test.mode)
			creation, etag := createHTTPServer(t, harness, fixture.URL(), test.mode)
			harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
			server := waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
				return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent && server.Catalog.ActiveToolCount == 2
			})
			require.NotNil(t, server.Catalog.DurableRevision)
			require.NotNil(t, server.Catalog.ActiveRevision)
			assert.Equal(t, *server.Catalog.DurableRevision, *server.Catalog.ActiveRevision)

			var catalog contract.CatalogPage
			response := harness.AdminJSON(http.MethodGet, "/api/v1/catalog", "", nil, &catalog)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.NoError(t, response.Body.Close())
			require.Len(t, catalog.Items, 2)
			assert.ElementsMatch(t, []string{"http-alpha", "http-beta"}, []string{catalog.Items[0].UpstreamName, catalog.Items[1].UpstreamName})

			response = harness.Request(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{"Authorization": "Bearer mgw_agent_fixture", "Content-Type": contract.MediaTypeJSON})
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			_ = readResponseBody(t, response)

			assertHTTPFixtureWire(t, fixture, test.mode)
			if test.mode == "modern" {
				proveBlockedHTTPCancellation(t, harness, fixture, creation.Server.ID, etag)
			} else {
				proveHTTPSessionLossWithdrawal(t, harness, fixture, creation.Server.ID, etag)
				harness.Stop(syscall.SIGTERM)
			}
		})
	}
}

func TestGatewayBinaryWithdrawsHTTPOnTransportLoss(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	fixture := newRawHTTPFixture(t, "modern")
	creation, etag := createHTTPServer(t, harness, fixture.URL(), "modern")
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveToolCount == 2
	})
	fixture.Close()
	refresh := createServerOperation(t, harness, creation.Server.ID, etag, string(contract.OperationRefreshCatalog), "transport-loss")
	harness.WaitOperation(creation.Server.ID, refresh.ID, contract.OperationFailed)
	server := waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State != contract.RuntimeActive && server.Runtime.Reason != nil && *server.Runtime.Reason == contract.ReasonConnectivity && server.Runtime.RuntimeID == nil && server.Catalog.ActiveRevision == nil && server.Catalog.ActiveToolCount == 0
	})
	assert.Equal(t, contract.ActiveCatalogUnavailable, server.Catalog.ActiveState)
	assert.Equal(t, contract.DurableCatalogUnavailable, server.Catalog.DurableState)
}

func createHTTPServer(t *testing.T, harness *gatewayHarness, endpoint, mode string) (stdioCreation, string) {
	t.Helper()
	request := map[string]any{
		"namespace": "http-" + mode, "display_name": "HTTP " + mode, "enabled": true,
		"transport": map[string]any{
			"kind": "streamable_http", "url": endpoint, "protocol_mode": mode,
			"authentication": map[string]string{"mode": "none"},
		},
	}
	contents, err := json.Marshal(request)
	require.NoError(t, err)
	var creation stdioCreation
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers", string(contents), map[string]string{"Idempotency-Key": "http-" + mode}, &creation)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	etag := response.Header.Get("ETag")
	require.NoError(t, response.Body.Close())
	require.NotNil(t, creation.Operation)
	require.NotEmpty(t, etag)
	return creation, etag
}

func createServerOperation(t *testing.T, harness *gatewayHarness, serverID, etag, kind, key string) contract.ServerOperation {
	t.Helper()
	var mutation contract.ServerOperationMutation
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers/"+serverID+"/operations", `{"kind":"`+kind+`"}`, map[string]string{"If-Match": etag, "Idempotency-Key": key}, &mutation)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.NoError(t, response.Body.Close())
	return mutation.Operation
}

func proveBlockedHTTPCancellation(t *testing.T, harness *gatewayHarness, fixture *rawHTTPFixture, serverID, etag string) {
	t.Helper()
	blocked, cancelled := fixture.ArmBlockedList()
	_ = createServerOperation(t, harness, serverID, etag, string(contract.OperationRefreshCatalog), "blocked-refresh")
	awaitFixtureSignal(t, blocked, "catalog request did not block")
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	awaitFixtureSignal(t, cancelled, "blocked HTTP exchange was not cancelled")
	result, err := harness.process.Wait()
	harness.process = nil
	require.NoError(t, err, "Gateway shutdown: %s", result.Stderr)
	assert.Equal(t, 0, result.ExitCode)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
}

func proveHTTPSessionLossWithdrawal(t *testing.T, harness *gatewayHarness, fixture *rawHTTPFixture, serverID, etag string) {
	t.Helper()
	fixture.LoseSession()
	refresh := createServerOperation(t, harness, serverID, etag, string(contract.OperationRefreshCatalog), "session-loss")
	harness.WaitOperation(serverID, refresh.ID, contract.OperationFailed)
	server := waitForStdioServer(t, harness, serverID, func(server stdioServerView) bool {
		return server.Runtime.State != contract.RuntimeActive && server.Runtime.Reason != nil && *server.Runtime.Reason == contract.ReasonConnectivity && server.Runtime.RuntimeID == nil && server.Catalog.ActiveRevision == nil && server.Catalog.ActiveToolCount == 0
	})
	assert.Equal(t, contract.ActiveCatalogUnavailable, server.Catalog.ActiveState)
	assert.Equal(t, contract.DurableCatalogUnavailable, server.Catalog.DurableState)
}

func assertHTTPFixtureWire(t *testing.T, fixture *rawHTTPFixture, mode string) {
	t.Helper()
	parsed, err := url.Parse(fixture.URL())
	require.NoError(t, err)
	events := fixture.Events()
	require.NotEmpty(t, events)
	methods := make([]string, len(events))
	connections := make(map[string]struct{}, len(events))
	for index, event := range events {
		methods[index] = event.Method
		assert.Equal(t, event.Method, event.MethodHeader)
		assert.Equal(t, parsed.Host, event.Host)
		assert.Equal(t, contract.MediaTypeJSON, event.ContentType)
		assert.Equal(t, "application/json, text/event-stream", event.Accept)
		assert.Empty(t, event.Authorization)
		assert.Empty(t, event.Cookie)
		assert.Empty(t, event.AcceptEncoding)
		assert.Empty(t, event.Forwarded)
		assert.True(t, event.Close)
		connections[event.Remote] = struct{}{}
	}
	assert.Len(t, connections, len(events), "hardened HTTP must not reuse connections")
	if mode == "modern" {
		assert.Equal(t, []string{"server/discover", "tools/list", "tools/list"}, methods)
		assert.Equal(t, []uint64{1, 2, 3}, []uint64{events[0].ID, events[1].ID, events[2].ID})
		for _, event := range events {
			assert.Equal(t, "2026-07-28", event.Protocol)
			assert.Empty(t, event.Session)
			assert.True(t, strings.Contains(event.Body, `"_meta"`))
		}
	} else {
		assert.Equal(t, []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/list"}, methods)
		assert.Equal(t, []uint64{1, 1, 0, 2, 3}, []uint64{events[0].ID, events[1].ID, events[2].ID, events[3].ID, events[4].ID})
		assert.Equal(t, "2026-07-28", events[0].Protocol)
		assert.Empty(t, events[0].Session)
		assert.True(t, strings.Contains(events[0].Body, `"_meta"`))
		assert.Equal(t, "2025-11-25", events[1].Protocol)
		assert.Empty(t, events[1].Session)
		for _, event := range events[1:] {
			assert.False(t, strings.Contains(event.Body, `"_meta"`))
		}
		for _, event := range events[2:] {
			assert.Equal(t, "2025-11-25", event.Protocol)
			assert.Equal(t, fixture.session, event.Session)
		}
	}
	toolEvents := make([]httpFixtureEvent, 0, 2)
	for _, event := range events {
		if event.Method == "tools/list" {
			toolEvents = append(toolEvents, event)
		}
	}
	require.Len(t, toolEvents, 2)
	assert.Equal(t, []string{"", "page-2"}, []string{toolEvents[0].Cursor, toolEvents[1].Cursor})
}

func awaitFixtureSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}
