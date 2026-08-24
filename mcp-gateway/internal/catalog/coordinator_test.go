package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollOffsetAndStrictEpochGrid(t *testing.T) {
	assert.Equal(t, 19*time.Second+438792422*time.Nanosecond, PollOffset(catalogInstallationID, "01ARZ3NDEKTSV4RRFFQ69G5FAW"))
	offset := 19*time.Second + 438792422*time.Nanosecond
	grid := 5 * time.Minute
	assert.Equal(t, time.Unix(0, int64(offset)).UTC(), NextPoll(time.Unix(0, 0).UTC(), offset))
	boundary := time.Unix(0, int64(grid+offset)).UTC()
	assert.Equal(t, boundary.Add(grid), NextPoll(boundary, offset))
	assert.Equal(t, boundary, NextPoll(boundary.Add(-time.Nanosecond), offset))
}

func TestCoordinatorActivationPublishesAndSchedulesFromCurrentClock(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	outcome := coordinator.Activate(context.Background(), candidate)
	assert.Equal(t, contract.ActiveCatalogCurrent, outcome.State)
	assert.Equal(t, runtimes.CatalogPublicationInstalled, outcome.Phase)
	assert.Equal(t, 1, client.calls)
	assert.Equal(t, contract.ActiveCatalogCurrent, registry.Status(server.ID).State)
	require.Len(t, scheduler.calls, 1)
	expected := NextPoll(clock.now, PollOffset(catalogInstallationID, server.ID)).Sub(clock.now)
	assert.Equal(t, expected, scheduler.calls[0].delay)

	clock.now = clock.now.Add(20 * time.Minute)
	scheduler.fire(t, 0)
	require.Eventually(t, func() bool { return client.callCount() == 2 }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return scheduler.count() == 2 }, time.Second, time.Millisecond)
	expected = NextPoll(clock.now, PollOffset(catalogInstallationID, server.ID)).Sub(clock.now)
	assert.Equal(t, expected, scheduler.call(1).delay)
}

func TestCoordinatorReturnsTypedDurableOnlyStorageOutcome(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	registry.beforeDescriptorRead = func() error { return errors.New("read latch") }
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)

	outcome := coordinator.Activate(context.Background(), candidate)

	assert.Equal(t, runtimes.CatalogPublicationDurableOnly, outcome.Phase)
	assert.Equal(t, runtimes.CatalogPostCommitStorage, outcome.Cause)
	assert.Equal(t, contract.ReasonConnectivity, *outcome.Reason)
	assert.Equal(t, contract.ActiveCatalogAbsent, outcome.State)
	durable, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	require.NotNil(t, durable.Revision)
	assert.Equal(t, "1", *durable.Revision)
	assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
	coordinator.Abandon(candidate)
}

func TestCoordinatorExplicitRefreshJoinsCurrentTraversal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`), started: make(chan struct{}), release: make(chan struct{})}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	results := make(chan runtimes.CatalogOutcome, 2)
	started := client.started
	go func() { results <- coordinator.Refresh(context.Background(), candidate) }()
	<-started
	go func() { results <- coordinator.Refresh(context.Background(), candidate) }()
	close(client.release)
	first, second := <-results, <-results
	assert.Equal(t, contract.ActiveCatalogCurrent, first.State)
	assert.Equal(t, contract.ActiveCatalogCurrent, second.State)
	assert.Equal(t, 1, client.callCount())
}

func TestCoordinatorHealthyFailureRetainsStaleAndWithdrawalCancelsTimer(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	assert.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(context.Background(), candidate).State)
	client.mu.Lock()
	client.err = ErrUnavailable
	client.mu.Unlock()
	failure := coordinator.Refresh(context.Background(), candidate)
	assert.Equal(t, contract.ActiveCatalogStale, failure.State)
	assert.Equal(t, contract.ActiveCatalogStale, registry.Status(server.ID).State)
	assert.Equal(t, int64(1), registry.Status(server.ID).ToolCount)
	durable, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.DurableCatalogStale, durable.State)
	assert.Equal(t, int64(1), durable.IssueCount)

	coordinator.Withdraw(candidate, contract.ActiveCatalogUnavailable)
	assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(server.ID).State)
	assert.True(t, scheduler.last().stopped)
	coordinator.Shutdown()
}

func TestCoordinatorWithdrawalCancelsTraversalAndPreventsLatePublication(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &cancellingCoordinatorClient{started: make(chan struct{})}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	result := make(chan runtimes.CatalogOutcome, 1)
	go func() { result <- coordinator.Refresh(context.Background(), candidate) }()
	<-client.started
	coordinator.Withdraw(candidate, contract.ActiveCatalogUnavailable)
	outcome := <-result
	assert.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
	assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(server.ID).State)
	assert.Nil(t, registry.Status(server.ID).Revision)
	assert.Equal(t, activeProcessID+"-1", registry.Summary().ActiveGeneration)
}

type cancellingCoordinatorClient struct{ started chan struct{} }

func (client *cancellingCoordinatorClient) Request(ctx context.Context, _ string, _ json.RawMessage, _ string) (downstream.Response, error) {
	close(client.started)
	<-ctx.Done()
	return downstream.Response{}, ctx.Err()
}

func coordinatorCandidate(t *testing.T, repository *servers.Repository, server servers.Server) runtimes.Candidate {
	t.Helper()
	authority, err := repository.Authority(context.Background(), server.ID)
	require.NoError(t, err)
	return runtimes.Candidate{Server: server, Authority: authority, RuntimeID: "runtime-1", Generation: 1}
}

type coordinatorClient struct {
	mu      sync.Mutex
	result  json.RawMessage
	err     error
	calls   int
	started chan struct{}
	release chan struct{}
}

func (client *coordinatorClient) Request(context.Context, string, json.RawMessage, string) (downstream.Response, error) {
	client.mu.Lock()
	client.calls++
	result, err := append(json.RawMessage(nil), client.result...), client.err
	started, release := client.started, client.release
	if started != nil {
		close(started)
		client.started = nil
	}
	client.mu.Unlock()
	if release != nil {
		<-release
	}
	return downstream.Response{Result: result}, err
}
func (client *coordinatorClient) callCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

type catalogTimer struct {
	callback func()
	stopped  bool
}

func (timer *catalogTimer) Stop() bool { timer.stopped = true; return true }

type catalogScheduledCall struct {
	delay time.Duration
	timer *catalogTimer
}

type catalogScheduler struct {
	mu    sync.Mutex
	calls []catalogScheduledCall
}

func newCatalogScheduler() *catalogScheduler { return new(catalogScheduler) }
func (scheduler *catalogScheduler) AfterFunc(delay time.Duration, callback func()) runtimes.Timer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	timer := &catalogTimer{callback: callback}
	scheduler.calls = append(scheduler.calls, catalogScheduledCall{delay: delay, timer: timer})
	return timer
}
func (scheduler *catalogScheduler) fire(t *testing.T, index int) {
	t.Helper()
	call := scheduler.call(index)
	require.False(t, call.timer.stopped)
	call.timer.callback()
}
func (scheduler *catalogScheduler) count() int {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return len(scheduler.calls)
}
func (scheduler *catalogScheduler) call(index int) catalogScheduledCall {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.calls[index]
}
func (scheduler *catalogScheduler) last() *catalogTimer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.calls[len(scheduler.calls)-1].timer
}
