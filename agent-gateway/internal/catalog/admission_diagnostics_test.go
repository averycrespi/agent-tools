package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogFailureDoesNotClassifySecondaryCapacityAsPrimary(t *testing.T) {
	repository, servers, clock, store := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(coordinator.Shutdown)
	candidate := coordinatorCandidate(t, servers, server)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	var outcome runtimes.CatalogOutcome
	require.NoError(t, store.Mutate(t.Context(), func(*sql.Tx) error {
		outcome = coordinator.failure(candidate, runtimes.CatalogTraversalRefresh, "traverse catalog", errors.New(storage.ErrMutationBusy.Error()), nil)
		return nil
	}))
	assert.Equal(t, "traverse catalog", outcome.DiagnosticDetail.Operation)
	assert.Contains(t, outcome.DiagnosticDetail.Explanation, "set durable catalog stale")
	assert.Contains(t, outcome.DiagnosticDetail.Explanation, storage.ErrMutationBusy.Error())
	assert.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
	assert.Equal(t, 1, client.callCount())
}

func TestCatalogAuditSettlementRefusalKeepsPrimaryStageAndGrid(t *testing.T) {
	for _, intent := range []runtimes.CatalogTraversalIntent{runtimes.CatalogTraversalInitial, runtimes.CatalogTraversalPoll} {
		t.Run(string(intent), func(t *testing.T) {
			repository, servers, clock, store := newCatalogRepository(t)
			server := createCatalogServer(t, servers, "sample")
			registry, err := NewActiveRegistry(repository, clock, activeProcessID)
			require.NoError(t, err)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`), started: started, release: release}
			scheduler := newCatalogScheduler()
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil) })
			coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Diagnostics: adapter, DiagnosticReference: func(string) uint64 { return 1 }})
			require.NoError(t, err)
			t.Cleanup(func() { unblock(); coordinator.Shutdown() })
			candidate := coordinatorCandidate(t, servers, server)
			results := make(chan runtimes.CatalogOutcome, 1)
			go func() { results <- coordinator.run(t.Context(), candidate, intent) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("catalog traversal did not start")
			}
			var outcome runtimes.CatalogOutcome
			require.NoError(t, store.Mutate(context.Background(), func(*sql.Tx) error {
				unblock()
				exhaustCatalogAuditWaits(t, scheduler)
				select {
				case outcome = <-results:
					return nil
				case <-time.After(5 * time.Second):
					return errors.New("catalog audit settlement did not finish")
				}
			}))
			assert.Equal(t, "settle refresh audit (capacity)", outcome.DiagnosticDetail.Operation)
			assert.Equal(t, server.DisplayName+" ("+server.ID+")", outcome.DiagnosticDetail.Resource)
			assert.Equal(t, runtimes.CatalogRuntimeHealthy, outcome.RuntimeHealth)
			assert.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
			require.NotNil(t, outcome.Reason)
			assert.Equal(t, contract.ReasonConnectivity, *outcome.Reason)
			assert.Equal(t, 1, client.callCount())
			assert.Zero(t, registry.Status(server.ID).ToolCount)
			require.Equal(t, 4, scheduler.count())
			pollAt := NextPoll(clock.now, PollOffset(catalogInstallationID, server.ID))
			assert.Equal(t, pollAt.Sub(clock.now), scheduler.call(3).delay)
			clock.now = pollAt
			scheduler.fire(t, 3)
			require.Eventually(t, func() bool { return scheduler.count() == 5 }, time.Second, time.Millisecond)
			assert.Equal(t, 2, client.callCount())
			assert.Equal(t, contract.ActiveCatalogCurrent, registry.Status(server.ID).State)
			coordinator.Shutdown()
			require.True(t, adapter.Finish(nil))
			if intent == runtimes.CatalogTraversalPoll {
				assert.Contains(t, output.String(), `"operation":"settle refresh audit (capacity)"`)
				assert.Contains(t, output.String(), `"resource":"`+server.DisplayName+" ("+server.ID+`)"`)
			}
		})
	}
}
