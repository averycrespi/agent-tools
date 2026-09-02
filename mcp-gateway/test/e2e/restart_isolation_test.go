//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryRestartReconstructsFreshAndIsolatesTwoServers(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer func() {
		if harness.process != nil {
			harness.Stop(syscall.SIGTERM)
		}
	}()

	executable, err := os.Executable()
	require.NoError(t, err)
	eventsPath := filepath.Join(t.TempDir(), "restart-events.jsonl")
	stdioCreation := createStdioServer(t, harness, executable, "modern", filepath.Join(t.TempDir(), "marker"), eventsPath)
	harness.WaitOperation(stdioCreation.Server.ID, stdioCreation.Operation.ID, contract.OperationSucceeded)
	stdioInitial := waitForStdioServer(t, harness, stdioCreation.Server.ID, activeCatalog)
	stdioEvents := waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 1
	})
	stdioInitialPID := fixtureEvents(stdioEvents, "start", "")[0].PID

	fixture := newRawHTTPFixture(t, "auto")
	httpCreation, httpETag := createHTTPServer(t, harness, fixture.URL(), "auto")
	harness.WaitOperation(httpCreation.Server.ID, httpCreation.Operation.ID, contract.OperationSucceeded)
	httpInitial := waitForStdioServer(t, harness, httpCreation.Server.ID, activeCatalog)
	require.NotNil(t, httpInitial.Runtime.RuntimeID)
	require.NotNil(t, httpInitial.Catalog.DurableRevision)
	initialSession := fixture.Session()
	require.NotEmpty(t, initialSession)

	blockedRefresh := fixture.Arm("tools/list")
	refresh := createServerOperation(t, harness, httpCreation.Server.ID, httpETag, string(contract.OperationRefreshCatalog), "restart-interrupted-refresh")
	awaitFixtureSignal(t, blockedRefresh.entered, "pre-restart refresh did not block")
	assertOperationState(t, harness, httpCreation.Server.ID, refresh.ID, contract.OperationRunning)

	require.NoError(t, harness.process.Signal(syscall.SIGKILL))
	crashed, waitErr := harness.process.Wait()
	harness.process = nil
	require.Error(t, waitErr)
	assert.NotZero(t, crashed.ExitCode)
	awaitFixtureSignal(t, blockedRefresh.cancelled, "crash did not cancel blocked catalog request")
	waitForProcessExit(t, stdioInitialPID)
	freshHTTPEventStart := len(fixture.Events())

	reconstruction := fixture.Arm("server/discover")
	harness.Start()
	ready := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	require.NoError(t, ready.Body.Close())
	awaitFixtureSignal(t, reconstruction.entered, "HTTP reconstruction did not reach transport barrier")

	interrupted := harness.WaitOperation(httpCreation.Server.ID, refresh.ID, contract.OperationInterrupted)
	require.NotNil(t, interrupted.Reason)
	assert.Equal(t, contract.ReasonInterrupted, *interrupted.Reason)
	httpBlocked := waitForStdioServer(t, harness, httpCreation.Server.ID, func(server stdioServerView) bool {
		return server.Catalog.ActiveRevision == nil && server.Catalog.ActiveToolCount == 0 && server.Catalog.DurableRevision != nil
	})
	assert.Equal(t, *httpInitial.Catalog.DurableRevision, *httpBlocked.Catalog.DurableRevision)
	assert.Equal(t, int64(2), httpBlocked.Catalog.DurableToolCount)

	stdioEvents = waitForFixtureEvents(t, eventsPath, func(events []stdioFixtureEvent) bool {
		return countFixtureEvents(events, "start", "") == 2 && countFixtureEvents(events, "request", "tools/list") == 4
	})
	stdioStarts := fixtureEvents(stdioEvents, "start", "")
	assert.NotEqual(t, stdioInitialPID, stdioStarts[1].PID)
	stdioReconstructed := waitForStdioServer(t, harness, stdioCreation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Runtime.RuntimeID != nil && server.Runtime.Reconciliation.InUse == 0
	})
	require.NotNil(t, stdioInitial.Runtime.RuntimeID)
	assert.NotEqual(t, *stdioInitial.Runtime.RuntimeID, *stdioReconstructed.Runtime.RuntimeID)
	if !activeCatalog(stdioReconstructed) {
		reload := createServerOperation(t, harness, stdioCreation.Server.ID, contract.ServerETag(stdioCreation.Server.ID, "1"), string(contract.OperationReload), "restart-catalog-recovery")
		harness.WaitOperation(stdioCreation.Server.ID, reload.ID, contract.OperationSucceeded)
		stdioReconstructed = waitForStdioServer(t, harness, stdioCreation.Server.ID, func(server stdioServerView) bool {
			return activeCatalog(server) && server.Runtime.Reconciliation.InUse == 0
		})
	}

	reconstruction.Release()
	httpReconstructed := waitForStdioServer(t, harness, httpCreation.Server.ID, func(server stdioServerView) bool {
		return activeCatalog(server) && server.Runtime.Reconciliation.InUse == 0
	})
	assert.NotEqual(t, *httpInitial.Runtime.RuntimeID, *httpReconstructed.Runtime.RuntimeID)
	assert.NotEqual(t, initialSession, fixture.Session())
	freshHTTPEvents := fixture.Events()[freshHTTPEventStart:]
	assert.Equal(t, []string{"server/discover", "initialize", "notifications/initialized", "tools/list", "tools/list"}, fixtureMethods(freshHTTPEvents))

	blockedIsolation := fixture.Arm("tools/list")
	httpRefresh := createServerOperation(t, harness, httpCreation.Server.ID, httpETag, string(contract.OperationRefreshCatalog), "blocked-isolation-refresh")
	awaitFixtureSignal(t, blockedIsolation.entered, "isolated HTTP refresh did not block")
	stdioDuringBlock := waitForStdioServer(t, harness, stdioCreation.Server.ID, activeCatalog)
	assert.Equal(t, *stdioReconstructed.Runtime.RuntimeID, *stdioDuringBlock.Runtime.RuntimeID)
	blockedIsolation.Release()
	harness.WaitOperation(httpCreation.Server.ID, httpRefresh.ID, contract.OperationSucceeded)
	waitForStdioServer(t, harness, httpCreation.Server.ID, func(server stdioServerView) bool {
		return activeCatalog(server) && server.Runtime.Reconciliation.InUse == 0
	})

	fixture.LoseSession()
	failedHTTP := createServerOperation(t, harness, httpCreation.Server.ID, httpETag, string(contract.OperationRefreshCatalog), "isolated-session-loss")
	harness.WaitOperation(httpCreation.Server.ID, failedHTTP.ID, contract.OperationFailed)
	waitForStdioServer(t, harness, httpCreation.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.RuntimeID == nil && server.Catalog.ActiveRevision == nil && server.Catalog.ActiveToolCount == 0
	})
	stdioAfterFailure := waitForStdioServer(t, harness, stdioCreation.Server.ID, activeCatalog)
	assert.Equal(t, *stdioReconstructed.Runtime.RuntimeID, *stdioAfterFailure.Runtime.RuntimeID)
	assert.Equal(t, *stdioDuringBlock.Catalog.ActiveRevision, *stdioAfterFailure.Catalog.ActiveRevision)
}
