//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeRestartInvalidatesAdminSession(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	ready := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	session := harness.Request(http.MethodPost, "/api/v1/admin-sessions", `{}`, map[string]string{
		"Authorization": "Bearer " + harness.bearer, "Content-Type": contract.MediaTypeJSON,
	})
	require.Equal(t, http.StatusCreated, session.StatusCode)
	cookies := session.Cookies()
	require.Len(t, cookies, 1)
	_ = session.Body.Close()
	result := harness.Stop(syscall.SIGTERM)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, 1, strings.Count(string(result.Stdout), "Gateway started successfully."))
	assert.Empty(t, result.Stderr)

	harness.serveArgs = append(harness.serveArgs, "--output", "json")
	harness.Start()
	stale := harness.Request(http.MethodGet, "/api/v1/system-status", "", map[string]string{
		"Cookie": cookies[0].String(), "Origin": "http://" + harness.authority,
	})
	assert.Equal(t, http.StatusUnauthorized, stale.StatusCode)
	_ = stale.Body.Close()
	result = harness.Stop(os.Interrupt)
	assert.Empty(t, result.Stderr)
	lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	require.Len(t, lines, 1)
	var startup struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &startup))
	assert.True(t, startup.OK)
	assert.Equal(t, "serve", startup.Operation)
}

func TestServeFirstSignalDeadlineRetainsUncleanMarker(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	blocked, err := net.Dial("tcp", harness.authority)
	require.NoError(t, err)
	_, err = fmt.Fprintf(blocked, "POST /api/v1/backups HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nIdempotency-Key: deadline\r\nContent-Length: 100\r\n\r\n", harness.authority, harness.bearer)
	require.NoError(t, err)
	waitForAdminOccupancy(t, harness, 2)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, harness.authority)
	result, waitErr := harness.process.Wait()
	harness.process = nil
	require.Error(t, waitErr)
	assert.Equal(t, 7, result.ExitCode)
	assert.Contains(t, string(result.Stderr), "clean shutdown could not be confirmed")
	assert.Equal(t, 1, strings.Count(string(result.Stdout), "Gateway started successfully."))
	_, markerErr := os.Stat(filepath.Join(harness.root, "run.unclean"))
	require.NoError(t, markerErr)
	require.NoError(t, blocked.Close())

	harness.Start()
	harness.Stop(syscall.SIGTERM)
	_, markerErr = os.Stat(filepath.Join(harness.root, "run.unclean"))
	require.ErrorIs(t, markerErr, os.ErrNotExist)
}

func TestServeSecondSignalForcesImmediateExit(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	blocked, err := net.Dial("tcp", harness.authority)
	require.NoError(t, err)
	defer func() { _ = blocked.Close() }()
	_, err = fmt.Fprintf(blocked, "POST /api/v1/backups HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nIdempotency-Key: forced\r\nContent-Length: 100\r\n\r\n", harness.authority, harness.bearer)
	require.NoError(t, err)
	waitForAdminOccupancy(t, harness, 2)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, harness.authority)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	type waitResult struct {
		result testutil.ProcessResult
		err    error
	}
	finished := make(chan waitResult, 1)
	go func() {
		result, waitErr := harness.process.Wait()
		finished <- waitResult{result: result, err: waitErr}
	}()
	select {
	case observed := <-finished:
		harness.process = nil
		require.Error(t, observed.err)
		assert.Equal(t, 2, observed.result.ExitCode)
	case <-time.After(3 * time.Second):
		t.Fatal("second signal did not force immediate exit")
	}
	_, markerErr := os.Stat(filepath.Join(harness.root, "run.unclean"))
	require.NoError(t, markerErr)
}

func TestServeLateHTTPCompletionAfterExitIsFenced(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	fixture := newRawHTTPFixture(t, "modern")
	creation, etag := createHTTPServer(t, harness, fixture.URL(), "modern")
	harness.WaitOperation(creation.Server.ID, creation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, creation.Server.ID, activeCatalog)
	late := fixture.ArmLateBlockedList()
	_ = createServerOperation(t, harness, creation.Server.ID, etag, string(contract.OperationRefreshCatalog), "late-signal-refresh")
	awaitFixtureSignal(t, late.entered, "late HTTP refresh did not block")
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	awaitFixtureSignal(t, late.cancelled, "drain did not cancel late HTTP refresh")
	result, waitErr := harness.process.Wait()
	harness.process = nil
	require.NoError(t, waitErr, "Gateway shutdown: %s", result.Stderr)

	eventCount := len(fixture.Events())
	late.Release()
	awaitFixtureSignal(t, late.completed, "late HTTP handler did not complete after process exit")
	assert.Equal(t, eventCount, len(fixture.Events()))
}

func TestServeFirstSignalDrainsActiveStdioAndHTTP(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	executable, err := os.Executable()
	require.NoError(t, err)
	eventsPath := filepath.Join(t.TempDir(), "drain-events.jsonl")
	stdio := createStdioServer(t, harness, executable, "blocked-stop", filepath.Join(t.TempDir(), "marker"), eventsPath)
	harness.WaitOperation(stdio.Server.ID, stdio.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, stdio.Server.ID, activeCatalog)
	stdioEvents := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 1
	})
	stdioPID := fixtureEvents(stdioEvents, "start", "")[0].PID

	fixture := newRawHTTPFixture(t, "modern")
	httpCreation, etag := createHTTPServer(t, harness, fixture.URL(), "modern")
	harness.WaitOperation(httpCreation.Server.ID, httpCreation.Operation.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, httpCreation.Server.ID, activeCatalog)
	var catalog contract.CatalogPage
	response := harness.AdminJSON(http.MethodGet, "/api/v1/catalog", "", nil, &catalog)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Len(t, catalog.Items, 4)
	require.NotEmpty(t, catalog.Catalog.ActiveGeneration)

	barrier := fixture.Arm("tools/list")
	_ = createServerOperation(t, harness, httpCreation.Server.ID, etag, string(contract.OperationRefreshCatalog), "signal-drain-refresh")
	awaitFixtureSignal(t, barrier.entered, "HTTP refresh did not block before drain")
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, harness.authority)
	awaitFixtureSignal(t, barrier.cancelled, "first signal did not cancel blocked HTTP work")
	result, waitErr := harness.process.Wait()
	harness.process = nil
	require.NoError(t, waitErr, "Gateway shutdown: %s", result.Stderr)
	assert.Equal(t, 0, result.ExitCode)
	waitForProcessExit(t, stdioPID)
	waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "eof", "") == 1 && countFixtureEvents(events, "signal", "") == 1
	})
	_, markerErr := os.Stat(filepath.Join(harness.root, "run.unclean"))
	require.ErrorIs(t, markerErr, os.ErrNotExist)
}

func TestEnabledServerFailureDoesNotRedefineReadiness(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	created := harness.AdminJSON(http.MethodPost, "/api/v1/servers", `{"namespace":"failing","display_name":"Failing","enabled":true,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`, map[string]string{"Idempotency-Key": "failing-server"}, nil)
	require.Equal(t, http.StatusCreated, created.StatusCode)
	var creation struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.NewDecoder(created.Body).Decode(&creation))
	_ = created.Body.Close()

	var observedState contract.RuntimeState
	var observedReason *contract.PublicReason
	require.Eventually(t, func() bool {
		response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+creation.Server.ID, "", nil, nil)
		defer func() { _ = response.Body.Close() }()
		var server struct {
			Runtime struct {
				State  contract.RuntimeState  `json:"state"`
				Reason *contract.PublicReason `json:"reason"`
			} `json:"runtime"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&server) != nil {
			return false
		}
		observedState, observedReason = server.Runtime.State, server.Runtime.Reason
		return observedState == contract.RuntimeRetryWait && observedReason != nil
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, contract.ReasonConnectivity, *observedReason)
	ready := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	statusResponse := harness.AdminJSON(http.MethodGet, "/api/v1/system-status", "", nil, nil)
	require.Equal(t, http.StatusOK, statusResponse.StatusCode)
	var status contract.SystemStatus
	require.NoError(t, json.NewDecoder(statusResponse.Body).Decode(&status))
	_ = statusResponse.Body.Close()
	assert.Equal(t, int64(1), status.Limits.ServerIdentities.InUse)
	assert.Equal(t, int64(1), status.Limits.Servers.InUse)
	assert.Equal(t, int64(1), status.Limits.S2IdempotencyRecords.InUse)
	assert.Zero(t, status.Limits.DownstreamRuntimes.InUse)
}

func waitForAdminOccupancy(t *testing.T, harness *gatewayHarness, minimum int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("blocked request did not occupy admin capacity")
		default:
		}
		response := harness.AdminJSON(http.MethodGet, "/api/v1/system-status", "", nil, nil)
		contents, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		var status contract.SystemStatus
		if response.StatusCode == http.StatusOK && json.Unmarshal(contents, &status) == nil && status.Limits.HTTPAdmin.InUse >= minimum {
			return
		}
	}
}

func waitForListenerClose(t *testing.T, authority string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", authority, 10*time.Millisecond)
		if err != nil {
			return
		}
		_ = connection.Close()
		select {
		case <-deadline:
			t.Fatal("Gateway listener remained open during drain")
		default:
		}
	}
}
