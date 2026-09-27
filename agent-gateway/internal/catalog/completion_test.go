package catalog

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledRuntimeLossCompletesAfterWithdrawalAndRefreshJoin(t *testing.T) {
	repository, servers, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	candidate := coordinatorCandidate(t, servers, server)
	type completion struct {
		outcome     runtimes.CatalogOutcome
		operation   *string
		inUse       int64
		state       contract.ActiveCatalogState
		descriptors int
	}
	completions := make(chan completion, 2)
	var coordinator *Coordinator
	coordinator, err = NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }, Complete: func(_ runtimes.Candidate, outcome runtimes.CatalogOutcome, operation *string) {
		completions <- completion{outcome: outcome, operation: operation, inUse: coordinator.ServerStatus(server.ID).InUse, state: registry.Status(server.ID).State, descriptors: len(registry.CurrentSnapshot().Descriptors)}
	}})
	require.NoError(t, err)
	defer coordinator.Shutdown()
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(t.Context(), candidate).State)
	started, release := make(chan struct{}), make(chan struct{})
	releasePoll := sync.OnceFunc(func() { close(release) })
	defer releasePoll()
	client.mu.Lock()
	client.err, client.started, client.release = downstream.ErrTransportClosed, started, release
	client.mu.Unlock()
	pollDone := make(chan struct{})
	go func() { scheduler.call(0).timer.callback(); close(pollDone) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("poll did not start")
	}
	operation := "refresh-operation"
	candidate.OperationID = &operation
	joined := make(chan runtimes.CatalogOutcome, 1)
	go func() { joined <- coordinator.Refresh(context.Background(), candidate) }()
	require.Eventually(t, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		work := coordinator.work[server.ID]
		return work != nil && work.operationID != nil
	}, time.Second, time.Millisecond)
	releasePoll()
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("poll did not finish")
	}
	result := <-joined
	assert.True(t, result.DiagnosticJoined)
	select {
	case completion := <-completions:
		require.NotNil(t, completion.operation)
		assert.Equal(t, operation, *completion.operation)
		assert.Equal(t, runtimes.CatalogRuntimeLost, completion.outcome.RuntimeHealth)
		require.NotNil(t, completion.outcome.RuntimeFailure)
		assert.True(t, completion.outcome.RuntimeFailure.Retryable)
		assert.Zero(t, completion.inUse, "callback ran before singleflight removal")
		assert.Equal(t, contract.ActiveCatalogUnavailable, completion.state)
		assert.Zero(t, completion.descriptors, "callback preceded discovery withdrawal")
	default:
		t.Fatal("background runtime loss was not handed to reconciliation")
	}
	assert.Empty(t, completions, "joined refresh duplicated the callback")
	assert.Equal(t, 2, client.callCount())
	assert.Equal(t, 1, scheduler.count(), "lost generation must not keep polling")
}
