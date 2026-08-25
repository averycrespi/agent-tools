//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stdioServerView struct {
	Runtime contract.ServerRuntime `json:"runtime"`
	Catalog contract.ServerCatalog `json:"catalog"`
}

type stdioCreation struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
	Operation *contract.ServerOperation `json:"operation"`
}

func TestGatewayBinaryActivatesAndPublishesStdioCatalog(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       string
		startCount int
	}{
		{name: "modern", mode: "modern", startCount: 1},
		{name: "fresh auto fallback", mode: "auto", startCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t)
			harness.Start()
			fixtureExecutable, err := os.Executable()
			require.NoError(t, err)
			eventsPath := filepath.Join(t.TempDir(), "stdio-events.jsonl")
			markerPath := filepath.Join(t.TempDir(), "fallback-marker")
			creation := createStdioServer(t, harness, fixtureExecutable, test.mode, markerPath, eventsPath)
			harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
			server := waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
				return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent && server.Catalog.ActiveToolCount == 2
			})
			require.NotNil(t, server.Runtime.RuntimeID)
			require.NotNil(t, server.Catalog.DurableRevision)
			require.NotNil(t, server.Catalog.ActiveRevision)
			assert.Equal(t, *server.Catalog.DurableRevision, *server.Catalog.ActiveRevision)
			assert.Equal(t, int64(2), server.Catalog.DurableToolCount)

			var catalog contract.CatalogPage
			response := harness.AdminJSON(http.MethodGet, "/api/v1/catalog", "", nil, &catalog)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, contract.AggregateCatalogCurrent, catalog.Catalog.ActiveState)
			require.Len(t, catalog.Items, 2)
			assert.ElementsMatch(t, []string{"alpha", "beta"}, []string{catalog.Items[0].UpstreamName, catalog.Items[1].UpstreamName})

			response = harness.Request(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, map[string]string{"Authorization": "Bearer mgw_agent_fixture", "Content-Type": contract.MediaTypeJSON})
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
			_ = readResponseBody(t, response)

			events := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
				return countFixtureEvents(events, "start", "") == test.startCount && countFixtureEvents(events, "request", "tools/list") == 2
			})
			starts := fixtureEvents(events, "start", "")
			require.Len(t, starts, test.startCount)
			selectedPID := starts[len(starts)-1].PID
			requests := fixtureEvents(events, "request", "")
			methods := make([]string, len(requests))
			for index, request := range requests {
				methods[index] = request.Method
				if request.Method == "tools/list" {
					assert.Equal(t, selectedPID, request.PID)
				}
			}
			if test.mode == "modern" {
				assert.Equal(t, []string{"server/discover", "tools/list", "tools/list"}, methods)
			} else {
				assert.Equal(t, starts[0].PID, starts[1].PriorPID)
				assert.False(t, starts[1].PriorAlive, "fallback selected process started before probe reap")
				assert.Equal(t, []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/list"}, methods)
			}
			toolRequests := fixtureEvents(events, "request", "tools/list")
			assert.Equal(t, []string{"", "page-2"}, []string{toolRequests[0].Cursor, toolRequests[1].Cursor})
			harness.Stop(syscall.SIGTERM)
			waitForProcessExit(t, selectedPID)
		})
	}
}

func TestGatewayBinaryWithdrawsStdioCatalogOnProcessAndOutputFailure(t *testing.T) {
	// Retried fixture faults reap themselves during negotiation, so the terminal fail-closed reason is cleanup proof rather than the initiating class.
	for _, test := range []struct {
		name   string
		mode   string
		reason contract.PublicReason
		kill   bool
	}{
		{name: "process exit", mode: "process-failure", reason: contract.ReasonStopUnconfirmed, kill: true},
		{name: "bounded output", mode: "output-failure", reason: contract.ReasonStopUnconfirmed},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t)
			harness.Start()
			defer harness.Stop(syscall.SIGTERM)
			fixtureExecutable, err := os.Executable()
			require.NoError(t, err)
			eventsPath := filepath.Join(t.TempDir(), "stdio-events.jsonl")
			creation := createStdioServer(t, harness, fixtureExecutable, test.mode, filepath.Join(t.TempDir(), "marker"), eventsPath)
			if test.kill {
				harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
				waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
					return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveToolCount == 2
				})
			}
			starts := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
				return countFixtureEvents(events, "start", "") == 1
			})
			pid := fixtureEvents(starts, "start", "")[0].PID
			if test.kill {
				require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
			}
			server := waitForStdioServer(t, harness, creation.Server.ID, func(server stdioServerView) bool {
				return server.Runtime.State == contract.RuntimeDegraded && server.Runtime.Reason != nil && *server.Runtime.Reason == test.reason && server.Catalog.ActiveRevision == nil && server.Catalog.ActiveToolCount == 0
			})
			assert.Equal(t, contract.ActiveCatalogUnavailable, server.Catalog.ActiveState)
			allEvents := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
				return countFixtureEvents(events, "start", "") >= 2
			})
			for _, started := range fixtureEvents(allEvents, "start", "") {
				waitForProcessExit(t, started.PID)
			}
		})
	}
}

func createStdioServer(t *testing.T, harness *gatewayHarness, executable, mode, markerPath, eventsPath string) stdioCreation {
	t.Helper()
	request := map[string]any{
		"namespace": "stdio-" + mode, "display_name": "Stdio " + mode, "enabled": true,
		"transport": map[string]any{
			"kind": "stdio", "executable": executable,
			"arguments":         []string{"-test.run=^TestE2EStdioFixtureProcess$", "--", "mcp", mode, markerPath, eventsPath},
			"working_directory": t.TempDir(), "environment": map[string]string{stdioFixtureEnvironment: "1"}, "secret_environment": map[string]string{},
		},
	}
	contents, err := json.Marshal(request)
	require.NoError(t, err)
	var creation stdioCreation
	response := harness.AdminJSON(http.MethodPost, "/api/v1/servers", string(contents), map[string]string{"Idempotency-Key": "stdio-" + mode}, &creation)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.NotNil(t, creation.Operation)
	return creation
}

func waitForStdioServer(t *testing.T, harness *gatewayHarness, serverID string, ready func(stdioServerView) bool) stdioServerView {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var current stdioServerView
	for {
		response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+serverID, "", nil, &current)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK && ready(current) {
			return current
		}
		select {
		case <-deadline.C:
			reason := contract.PublicReason("")
			if current.Runtime.Reason != nil {
				reason = *current.Runtime.Reason
			}
			t.Fatalf("server %s did not reach expected state; observed runtime=%+v reason=%s catalog=%+v", serverID, current.Runtime, reason, current.Catalog)
		case <-ticker.C:
		}
	}
}

func waitForFixtureEvents(t *testing.T, path string, ready func([]stdioFixtureEvent) bool) []stdioFixtureEvent {
	t.Helper()
	var current []stdioFixtureEvent
	require.Eventually(t, func() bool {
		file, err := os.Open(path)
		if err != nil {
			return false
		}
		defer func() { _ = file.Close() }()
		current = nil
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var event stdioFixtureEvent
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				return false
			}
			current = append(current, event)
		}
		return scanner.Err() == nil && ready(current)
	}, 5*time.Second, 10*time.Millisecond)
	return current
}

func fixtureEvents(events []stdioFixtureEvent, kind, method string) []stdioFixtureEvent {
	selected := make([]stdioFixtureEvent, 0)
	for _, event := range events {
		if event.Kind == kind && (method == "" || event.Method == method) {
			selected = append(selected, event)
		}
	}
	return selected
}

func countFixtureEvents(events []stdioFixtureEvent, kind, method string) int {
	return len(fixtureEvents(events, kind, method))
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool { return !processExists(pid) }, 5*time.Second, 10*time.Millisecond)
}
