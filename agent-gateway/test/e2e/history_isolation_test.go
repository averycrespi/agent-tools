//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGatewayBinaryHistoryLossReplacementAndRestart(t *testing.T) {
	for _, mode := range []string{"healthy", "absent", "damaged"} {
		t.Run(mode, func(t *testing.T) {
			harness := newGatewayHarness(t)
			historyPath := selectedTrafficPath(t, harness.root)
			const damaged = "retained damaged optional history"
			switch mode {
			case "absent":
				require.NoError(t, os.Rename(historyPath, historyPath+".retained"))
			case "damaged":
				require.NoError(t, os.WriteFile(historyPath, []byte(damaged), 0o600))
			}
			harness.Start()
			catalog := harness.SetupCurrentCatalog("history-isolation", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
			principal := harness.CreatePrincipal("History independent caller", contract.VisibilityAll)
			issued := harness.IssueCredential(principal)
			harness.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
			assertCallSuccess(t, harness.ModernCall(issued.Bearer, json.RawMessage(`"modern"`), "history-isolation.alpha", json.RawMessage(`{}`)))
			session, _ := harness.LegacyInitialize(issued.Bearer, json.RawMessage(`1`))
			assertCallSuccess(t, harness.LegacyCall(issued.Bearer, session, json.RawMessage(`"legacy"`), "history-isolation.alpha", json.RawMessage(`{}`)))
			require.Equal(t, 2, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))
			barrier := catalog.Fixture.Arm("tools/call")
			defer barrier.Release()
			completed := make(chan responseSnapshot, 1)
			go func() {
				completed <- harness.ModernCall(issued.Bearer, json.RawMessage(`"pinned"`), "history-isolation.alpha", json.RawMessage(`{}`))
			}()
			awaitFixtureSignal(t, barrier.entered, "call did not enter pinned downstream")
			transport := fmt.Sprintf(`{"transport":{"kind":"streamable_http","url":%q,"protocol_mode":"auto","authentication":{"mode":"none"}}}`, catalog.Fixture.URL())
			mutation, _ := patchServer(t, harness, catalog.ServerID, catalog.ETag, transport)
			require.NotNil(t, mutation.Operation)
			awaitFixtureSignal(t, barrier.cancelled, "replacement did not cancel old capability")
			barrier.Release()
			awaitFixtureSignal(t, barrier.completed, "old downstream handler did not settle")
			assertCallError(t, <-completed, json.RawMessage(`"pinned"`), contract.OutcomeUnknown, true)
			harness.WaitOperation(catalog.ServerID, mutation.Operation.ID, contract.OperationSucceeded)
			harness.WaitSettledOperation(catalog.ServerID, mutation.Operation.ID)
			require.Equal(t, 3, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"), "replacement must not reroute")
			var status contract.SystemStatus
			decodeSnapshot(t, harness.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil), http.StatusOK, &status)
			require.Zero(t, status.Limits.DownstreamDispatch.InUse, "history does not own downstream occupancy")
			harness.Stop(syscall.SIGTERM)
			harness.Start()
			waitForStdioServer(t, harness, catalog.ServerID, activeCatalog)
			require.Equal(t, 3, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"), "restart must not reconstruct execution from history")
			assertCallSuccess(t, harness.ModernCall(issued.Bearer, json.RawMessage(`"fresh"`), "history-isolation.alpha", json.RawMessage(`{}`)))
			require.Equal(t, 4, httpFixtureMethodCount(catalog.Fixture.Events(), "tools/call"))
			harness.Stop(syscall.SIGTERM)
			switch mode {
			case "absent":
				_, err := os.Lstat(historyPath)
				require.True(t, os.IsNotExist(err), "startup must not recreate missing history")
				_, err = os.Stat(historyPath + ".retained")
				require.NoError(t, err)
			case "damaged":
				contents, err := os.ReadFile(historyPath)
				require.NoError(t, err)
				require.Equal(t, damaged, string(contents), "restart must preserve damaged history")
			}
		})
	}
}
