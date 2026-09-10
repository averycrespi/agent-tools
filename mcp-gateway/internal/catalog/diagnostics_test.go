package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/stretchr/testify/require"
)

func TestJoinedFailingPollKeepsOneDiagnosticOwner(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "private-joined-catalog")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`)}
	scheduler := newCatalogScheduler()
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil) })
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, serverRepository, server)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	started, release := make(chan struct{}), make(chan struct{})
	client.mu.Lock()
	client.started, client.release, client.err = started, release, context.DeadlineExceeded
	client.mu.Unlock()
	pollDone := make(chan struct{})
	go func() { scheduler.call(0).timer.callback(); close(pollDone) }()
	<-started
	explicit := make(chan runtimes.CatalogOutcome, 1)
	joinedCandidate := candidate
	operationID := "private-joined-operation"
	joinedCandidate.OperationID = &operationID
	go func() { explicit <- coordinator.Refresh(t.Context(), joinedCandidate) }()
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		work := coordinator.work[server.ID]
		return work != nil && work.operationID != nil && *work.operationID == operationID
	}, time.Second, time.Millisecond, "explicit caller must join before the poll completes")
	close(release)
	joined := <-explicit
	<-pollDone
	require.True(t, joined.DiagnosticJoined, "joined lifecycle result defers diagnostic observation to its owner")
	require.Equal(t, runtimes.CatalogTraversalPoll, joined.Intent)
	require.Equal(t, contract.ActiveCatalogStale, joined.State)
	require.Equal(t, 2, client.callCount())
	require.Equal(t, 2, scheduler.count())
	coordinator.Shutdown()
	require.True(t, adapter.Finish(nil))
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"upstream_unhealthy"`)))
	require.Contains(t, output.String(), `"disposition":"retry_scheduled"`)
	require.NotContains(t, output.String(), `"disposition":"unknown"`)
}

type pollDiagnosticObserver func(diagnostics.Facts)

func (observer pollDiagnosticObserver) Reconciliation(facts diagnostics.Facts) { observer(facts) }

func TestCatalogPollDiagnosticsObserveTimeoutAndRecoveryWithoutChangingPolling(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "private-catalog-name")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"private-tool-canary","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Debug)
	t.Cleanup(func() { adapter.Finish(nil) })
	var observedScheduleCounts []int
	observer := pollDiagnosticObserver(func(facts diagnostics.Facts) {
		if facts.Event == diagnostics.UpstreamUnhealthy {
			observedScheduleCounts = append(observedScheduleCounts, scheduler.count())
		}
		adapter.Reconciliation(facts)
	})
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: observer, DiagnosticReference: func(string) uint64 { return 5 }})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, serverRepository, server)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll).State)
	client.err = errors.Join(context.DeadlineExceeded, errors.New("private-response-token-url-canary"))
	for range 2 {
		require.Equal(t, contract.ActiveCatalogStale, coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll).State)
	}
	client.err = nil
	for range 2 {
		require.Equal(t, contract.ActiveCatalogCurrent, coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll).State)
	}
	client.err = downstream.ErrSessionLost
	lost := coordinator.run(t.Context(), candidate, runtimes.CatalogTraversalPoll)
	require.Equal(t, contract.ActiveCatalogUnavailable, lost.State)
	require.Zero(t, lost.DiagnosticRetryDelay, "lost runtime must not claim a new catalog poll")
	require.Equal(t, 7, client.callCount())
	require.Equal(t, 6, scheduler.count(), "diagnostics do not add/remove polling work")
	require.Equal(t, []int{3, 4, 6}, observedScheduleCounts, "warnings observe the actual scheduling outcome, never the intended schedule")
	coordinator.Shutdown()
	require.True(t, adapter.Finish(nil))
	require.Equal(t, 2, bytes.Count(output.Bytes(), []byte(`"event":"upstream_unhealthy"`)))
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte(`"event":"upstream_recovered"`)))
	require.Contains(t, output.String(), `"phase":"tool_discovery"`)
	require.Contains(t, output.String(), `"reason":"timeout"`)
	var scheduled int
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'}) {
		var record struct {
			Event       string `json:"event"`
			Reason      string `json:"reason"`
			Disposition string `json:"disposition"`
			Delay       int64  `json:"delay_ms"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		if record.Event == "catalog_poll_scheduled" {
			require.Equal(t, scheduler.call(scheduled).delay.Milliseconds(), record.Delay)
			scheduled++
		}
		if record.Event == "upstream_unhealthy" {
			if record.Reason == "timeout" {
				require.Equal(t, "retry_scheduled", record.Disposition)
			} else {
				require.Equal(t, "unknown", record.Disposition)
			}
		}
	}
	require.Equal(t, 6, scheduled)
	for _, canary := range []string{server.ID, "private-catalog-name", "private-tool-canary", "private-response-token-url-canary"} {
		require.NotContains(t, output.String(), canary)
	}
}
