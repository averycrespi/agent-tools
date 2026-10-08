//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

// A real read snapshot deliberately outlives shutdown, so graceful closure is
// not accidentally tested only with an empty WAL. It has a finite fixture owner.
func TestGatewayBinaryTrafficCommittedWALRestarts(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "forced"}[forced], func(t *testing.T) {
			h := newGatewayHarness(t)
			path := selectedTrafficPath(t, h.root)
			h.Start()
			catalog := h.SetupCurrentCatalog("wal-restart", []fixtureTool{{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)}})
			principal := h.CreatePrincipal("WAL caller", contract.VisibilityAll)
			issued := h.IssueCredential(principal)
			h.CreateGrant(grantSpec{PrincipalID: principal.Resource.ID, Effect: contract.GrantAllow, ServerID: catalog.ServerID, UpstreamName: pointerTo("alpha")})
			calls := 0
			for cycle := 0; cycle < 2; cycle++ {
				waitTrafficStatus(t, h, func(s *contract.TrafficStatus) bool { return s.Ready })
				db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
				require.NoError(t, err)
				defer func() { require.NoError(t, db.Close()) }()
				tx, err := db.BeginTx(h.ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				var count int
				require.NoError(t, tx.QueryRowContext(h.ctx, `SELECT count(*) FROM invocations`).Scan(&count))
				before := trafficStatus(t, h).Delivery.Acknowledged
				assertCallSuccess(t, h.ModernCall(issued.Bearer, json.RawMessage(`"persist"`), "wal-restart.alpha", json.RawMessage(`{}`)))
				calls++
				waitTrafficStatus(t, h, func(s *contract.TrafficStatus) bool {
					return s.Delivery.Acknowledged > before && s.Delivery.QueueRecords == 0
				})
				acknowledged := h.readOnlyAuditObservations()
				require.NotEmpty(t, acknowledged)
				var interruptedID string
				var done <-chan error
				var barrier *httpFixtureBarrier
				if forced {
					barrier = catalog.Fixture.Arm("tools/call")
					defer barrier.Release()
					before = trafficStatus(t, h).Delivery.Acknowledged
					done = h.ModernCallDiscardResponse(issued.Bearer, json.RawMessage(`"interrupted"`), "wal-restart.alpha", json.RawMessage(`{}`))
					awaitFixtureSignal(t, barrier.entered, "interrupted call did not reach upstream")
					calls++
					waitTrafficStatus(t, h, func(s *contract.TrafficStatus) bool {
						return s.Delivery.Acknowledged > before && s.Delivery.QueueRecords == 0
					})
					for _, row := range h.readOnlyAuditObservations() {
						if row.TerminalClass == "" {
							interruptedID = row.InvocationID
						}
					}
					require.NotEmpty(t, interruptedID)
					require.NoError(t, h.process.Signal(syscall.SIGKILL))
					result, waitErr := h.process.Wait()
					h.process = nil
					h.results = append(h.results, result)
					require.Error(t, waitErr)
					assertSettledResult(t, result)
					barrier.Release()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("interrupted client did not settle")
					}
				} else {
					h.Stop(syscall.SIGTERM)
				}
				info, err := os.Stat(path + "-wal")
				require.NoError(t, err)
				require.Positive(t, info.Size(), "restart must exercise committed retained WAL")
				h.Start()
				waitTrafficStatus(t, h, func(s *contract.TrafficStatus) bool { return s.Ready })
				waitForStdioServer(t, h, catalog.ServerID, activeCatalog)
				require.Equal(t, calls, catalog.CallCount(), "restart must not replay upstream work")
				after := h.readOnlyAuditObservations()
				for _, old := range acknowledged {
					found := false
					for _, row := range after {
						if row.InvocationID == old.InvocationID {
							require.Equal(t, old, row)
							found = true
						}
					}
					require.True(t, found, "previously acknowledged history must survive")
				}
				if forced {
					found := false
					for _, row := range after {
						if row.InvocationID == interruptedID {
							require.Empty(t, row.TerminalClass)
							found = true
						}
					}
					require.True(t, found, "interrupted admission must not fabricate completion")
				}
				require.NoError(t, tx.Rollback())
				require.NoError(t, db.Close())
				before = trafficStatus(t, h).Delivery.Acknowledged
				assertCallSuccess(t, h.ModernCall(issued.Bearer, json.RawMessage(`"fresh"`), "wal-restart.alpha", json.RawMessage(`{}`)))
				calls++
				waitTrafficStatus(t, h, func(s *contract.TrafficStatus) bool {
					return s.Delivery.Acknowledged > before && s.Delivery.QueueRecords == 0
				})
				require.Equal(t, calls, catalog.CallCount())
				require.Greater(t, len(h.readOnlyAuditObservations()), len(after), "fresh acknowledgment requires persisted independent traffic")
			}
			h.Stop(syscall.SIGTERM)
		})
	}
}

func trafficStatus(t *testing.T, h *gatewayHarness) *contract.TrafficStatus {
	t.Helper()
	var status contract.SystemStatus
	decodeSnapshot(t, h.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil), http.StatusOK, &status)
	require.NotNil(t, status.Traffic)
	return status.Traffic
}
func waitTrafficStatus(t *testing.T, h *gatewayHarness, ready func(*contract.TrafficStatus) bool) {
	t.Helper()
	require.Eventually(t, func() bool { return ready(trafficStatus(t, h)) }, 10*time.Second, 10*time.Millisecond)
}
