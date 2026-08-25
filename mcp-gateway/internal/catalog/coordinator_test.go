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

func TestCoordinatorInitialAndNoPriorRefreshFailuresRemainHealthyAndRecoverable(t *testing.T) {
	for _, intent := range []runtimes.CatalogTraversalIntent{runtimes.CatalogTraversalInitial, runtimes.CatalogTraversalRefresh} {
		t.Run(string(intent), func(t *testing.T) {
			repository, serverRepository, clock, _ := newCatalogRepository(t)
			server := createCatalogServer(t, serverRepository, "sample")
			registry, err := NewActiveRegistry(repository, clock, activeProcessID)
			require.NoError(t, err)
			client := &coordinatorClient{err: ErrUnavailable}
			scheduler := newCatalogScheduler()
			candidate := coordinatorCandidate(t, serverRepository, server)
			coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
			require.NoError(t, err)
			var outcome runtimes.CatalogOutcome
			if intent == runtimes.CatalogTraversalInitial {
				outcome = coordinator.Activate(context.Background(), candidate)
			} else {
				outcome = coordinator.Refresh(context.Background(), candidate)
			}
			assert.Equal(t, intent, outcome.Intent)
			assert.Equal(t, runtimes.CatalogRuntimeHealthy, outcome.RuntimeHealth)
			assert.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
			assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(server.ID).State)
			assert.Zero(t, registry.Status(server.ID).ToolCount)
			durable, statusErr := repository.Status(context.Background(), server.ID)
			require.NoError(t, statusErr)
			assert.Equal(t, contract.DurableCatalogUnavailable, durable.State)
			require.Len(t, scheduler.calls, 1)
		})
	}
}

func TestCoordinatorPollAndExplicitRefreshShareOneExactTraversal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(context.Background(), candidate).State)
	client.mu.Lock()
	client.started = make(chan struct{})
	started := client.started
	client.release = make(chan struct{})
	release := client.release
	client.mu.Unlock()

	go scheduler.call(0).timer.callback()
	<-started
	explicit := make(chan runtimes.CatalogOutcome, 1)
	go func() { explicit <- coordinator.Refresh(context.Background(), candidate) }()
	close(release)
	outcome := <-explicit

	assert.Equal(t, runtimes.CatalogTraversalPoll, outcome.Intent)
	assert.Equal(t, contract.ActiveCatalogCurrent, outcome.State)
	assert.Equal(t, 2, client.callCount())
	require.Eventually(t, func() bool { return scheduler.count() == 2 }, time.Second, time.Millisecond)
}

func TestCoordinatorProjectsFirstPageOAuthDispositionWithoutRuntimeLoss(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeCatalogFirstPage, Metadata: []string{"https://resource.example/metadata"}}
	client := &coordinatorClient{err: disposition}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)

	outcome := coordinator.Activate(context.Background(), candidate)

	assert.Equal(t, contract.ActiveCatalogUnavailable, outcome.State)
	assert.Equal(t, runtimes.CatalogRuntimeHealthy, outcome.RuntimeHealth)
	assert.Same(t, disposition, outcome.OAuthChallenge)
	assert.Equal(t, 1, client.callCount())
}

func TestCatalogRuntimeFailureClassificationIsClosed(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		state  contract.RuntimeState
		reason contract.PublicReason
		lost   bool
	}{
		{name: "authentication", err: downstream.ErrAuthenticationRejected, state: contract.RuntimeAuthenticationRequired, reason: contract.ReasonAuthenticationRejected, lost: true},
		{name: "session", err: downstream.ErrSessionLost, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, lost: true},
		{name: "transport", err: downstream.ErrTransportClosed, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, lost: true},
		{name: "remote", err: downstream.ErrRemoteUnavailable, state: contract.RuntimeDegraded, reason: contract.ReasonConnectivity, lost: true},
		{name: "protocol", err: downstream.ErrInvalidMessage, state: contract.RuntimeDegraded, reason: contract.ReasonProtocolInvalid, lost: true},
		{name: "waiter_cancel", err: context.Canceled},
		{name: "healthy_catalog", err: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := catalogRuntimeFailure(errors.Join(ErrUnavailable, &requestFailure{err: test.err}), &coordinatorClient{})
			if !test.lost {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.True(t, failure.RuntimeLost)
			assert.Equal(t, test.state, failure.State)
			assert.Equal(t, test.reason, failure.Reason)
		})
	}
}

func TestCoordinatorRuntimeLossWithdrawsPriorSnapshotAndStopsPolling(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(context.Background(), candidate).State)
	client.mu.Lock()
	client.err = downstream.ErrSessionLost
	client.mu.Unlock()

	failure := coordinator.Refresh(context.Background(), candidate)

	assert.Equal(t, contract.ActiveCatalogUnavailable, failure.State)
	assert.Equal(t, contract.ReasonConnectivity, *failure.Reason)
	assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(server.ID).State)
	assert.Zero(t, registry.Status(server.ID).ToolCount)
	assert.True(t, scheduler.last().stopped)
	assert.Equal(t, 1, scheduler.count())
}

func TestCoordinatorCancelledWaiterDoesNotCancelExactTraversal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`), started: make(chan struct{}), release: make(chan struct{})}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	first := make(chan runtimes.CatalogOutcome, 1)
	started := client.started
	go func() { first <- coordinator.Refresh(context.Background(), candidate) }()
	<-started
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cancelled := coordinator.Refresh(cancelledCtx, candidate)

	require.NotNil(t, cancelled.Reason)
	assert.Equal(t, contract.ReasonCancelled, *cancelled.Reason)
	assert.Equal(t, int64(1), coordinator.ServerStatus(server.ID).InUse)
	assert.Equal(t, 1, client.callCount())
	close(client.release)
	assert.Equal(t, contract.ActiveCatalogCurrent, (<-first).State)
}

func TestCoordinatorMismatchedCandidateNeverJoinsCurrentTraversal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`), started: make(chan struct{}), release: make(chan struct{})}
	candidate := coordinatorCandidate(t, serverRepository, server)
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	first := make(chan runtimes.CatalogOutcome, 1)
	started := client.started
	go func() { first <- coordinator.Refresh(context.Background(), candidate) }()
	<-started
	replacement := candidate
	replacement.RuntimeID = "runtime-2"
	replacement.Generation++
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	mismatch := coordinator.Refresh(ctx, replacement)

	require.NotNil(t, mismatch.Reason)
	assert.Equal(t, contract.ReasonSuperseded, *mismatch.Reason)
	assert.Equal(t, 1, client.callCount())
	close(client.release)
	assert.Equal(t, contract.ActiveCatalogCurrent, (<-first).State)
}

func TestCoordinatorRuntimeLossIsolatedFromSecondServer(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	serverA := createCatalogServer(t, serverRepository, "a")
	serverB := createCatalogServer(t, serverRepository, "b")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	clients := map[string]*coordinatorClient{
		serverA.ID: {result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)},
		serverB.ID: {result: json.RawMessage(`{"tools":[{"name":"two","inputSchema":{"type":"object"}}]}`)},
	}
	scheduler := newCatalogScheduler()
	candidateA := coordinatorCandidate(t, serverRepository, serverA)
	candidateB := coordinatorCandidate(t, serverRepository, serverB)
	candidateB.RuntimeID = "runtime-2"
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(candidate runtimes.Candidate) (PageClient, bool) { return clients[candidate.Server.ID], true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(context.Background(), candidateA).State)
	require.Equal(t, contract.ActiveCatalogCurrent, coordinator.Activate(context.Background(), candidateB).State)
	clients[serverA.ID].mu.Lock()
	clients[serverA.ID].err = downstream.ErrTransportClosed
	clients[serverA.ID].mu.Unlock()

	failure := coordinator.Refresh(context.Background(), candidateA)

	assert.Equal(t, runtimes.CatalogRuntimeLost, failure.RuntimeHealth)
	assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(serverA.ID).State)
	assert.Equal(t, contract.ActiveCatalogCurrent, registry.Status(serverB.ID).State)
	assert.Equal(t, int64(1), registry.Status(serverB.ID).ToolCount)
	assert.Equal(t, contract.AggregateCatalogDegraded, registry.Summary().ActiveState)
	assert.Equal(t, int64(1), registry.Occupancy().InUse)
}

func TestCoordinatorWithdrawnWorkMustFinishBeforeReplacementTraversal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	oldClient := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`), started: make(chan struct{}), release: make(chan struct{})}
	newClient := &coordinatorClient{result: json.RawMessage(`{"tools":[]}`)}
	oldCandidate := coordinatorCandidate(t, serverRepository, server)
	newCandidate := oldCandidate
	newCandidate.RuntimeID = "runtime-2"
	newCandidate.Generation++
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: newCatalogScheduler(), Client: func(candidate runtimes.Candidate) (PageClient, bool) {
		if candidate.RuntimeID == oldCandidate.RuntimeID {
			return oldClient, true
		}
		return newClient, true
	}, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	oldResult := make(chan runtimes.CatalogOutcome, 1)
	oldStarted := oldClient.started
	go func() { oldResult <- coordinator.Refresh(context.Background(), oldCandidate) }()
	<-oldStarted
	coordinator.Withdraw(oldCandidate, contract.ActiveCatalogUnavailable)

	blockedReplacement := coordinator.Refresh(context.Background(), newCandidate)

	require.NotNil(t, blockedReplacement.Reason)
	assert.Equal(t, contract.ReasonSuperseded, *blockedReplacement.Reason)
	assert.Zero(t, newClient.callCount())
	close(oldClient.release)
	<-oldResult
	assert.Equal(t, contract.ActiveCatalogCurrent, coordinator.Refresh(context.Background(), newCandidate).State)
	assert.Equal(t, 1, newClient.callCount())
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
