package runtimes

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	mu          sync.Mutex
	servers     map[string]servers.Server
	authorities map[string]servers.AuthorityMetadata
	operations  map[string]servers.Operation
	nextID      int
	interrupted bool
}

func newFakeRepository(count int) *fakeRepository {
	repository := &fakeRepository{servers: make(map[string]servers.Server), authorities: make(map[string]servers.AuthorityMetadata), operations: make(map[string]servers.Operation)}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5F%02d", index)
		repository.servers[id] = servers.Server{ID: id, Namespace: fmt.Sprintf("server-%d", index), DisplayName: "Server", DesiredState: contract.DesiredServerEnabled, DesiredRevision: "1", CreatedAt: "2026-08-23T00:00:00Z", UpdatedAt: "2026-08-23T00:00:00Z"}
		repository.authorities[id] = zeroAuthority()
	}
	return repository
}

func zeroAuthority() servers.AuthorityMetadata {
	return servers.AuthorityMetadata{RegistrationRevision: "0", CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}}
}

func (repository *fakeRepository) Get(_ context.Context, id string) (servers.Server, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	server, ok := repository.servers[id]
	if !ok {
		return servers.Server{}, servers.ErrNotFound
	}
	return server, nil
}
func (repository *fakeRepository) ListServers(context.Context, *servers.SnapshotCursor, int) (servers.ServerPage, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	items := make([]servers.Server, 0, len(repository.servers))
	for _, server := range repository.servers {
		items = append(items, server)
	}
	return servers.ServerPage{Items: items}, nil
}
func (repository *fakeRepository) Authority(_ context.Context, id string) (servers.AuthorityMetadata, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	authority, ok := repository.authorities[id]
	if !ok {
		return servers.AuthorityMetadata{}, servers.ErrNotFound
	}
	return authority, nil
}
func (repository *fakeRepository) CreateOperation(_ context.Context, request servers.OperationRequest) (servers.OperationResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.nextID++
	id := fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G6F%02d", repository.nextID)
	operation := servers.Operation{ID: id, ServerID: request.ServerID, Kind: request.Kind, TargetDesiredRevision: request.ExpectedDesiredRevision, TargetCredentialRevisions: zeroAuthority().CredentialRevisions, State: contract.OperationScheduled, CreatedAt: "2026-08-23T00:00:00Z"}
	repository.operations[id] = operation
	return servers.OperationResult{Operation: operation}, nil
}
func (repository *fakeRepository) GetOperation(_ context.Context, id string) (servers.Operation, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, ok := repository.operations[id]
	if !ok {
		return servers.Operation{}, servers.ErrNotFound
	}
	return operation, nil
}
func (repository *fakeRepository) TransitionOperation(_ context.Context, id string, state contract.ServerOperationState, reason *contract.PublicReason) (servers.Operation, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, ok := repository.operations[id]
	if !ok {
		return servers.Operation{}, servers.ErrNotFound
	}
	operation.State, operation.Reason = state, reason
	repository.operations[id] = operation
	return operation, nil
}
func (repository *fakeRepository) InterruptNonterminal(context.Context) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.interrupted = true
	return nil
}
func (repository *fakeRepository) NewID() (string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.nextID++
	return fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G7F%02d", repository.nextID), nil
}
func (repository *fakeRepository) setRevision(id, revision string) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	server := repository.servers[id]
	server.DesiredRevision = revision
	repository.servers[id] = server
}

type blockingDriver struct {
	started chan Candidate
	release chan Outcome
	cleaned chan Candidate
}

func newBlockingDriver() *blockingDriver {
	return &blockingDriver{started: make(chan Candidate, 32), release: make(chan Outcome, 32), cleaned: make(chan Candidate, 32)}
}
func (driver *blockingDriver) Reconcile(ctx context.Context, candidate Candidate) Outcome {
	driver.started <- candidate
	select {
	case outcome := <-driver.release:
		return outcome
	case <-ctx.Done():
		return Outcome{}
	}
}
func (driver *blockingDriver) Cleanup(_ context.Context, candidate Candidate) {
	driver.cleaned <- candidate
}

func activeOutcome() Outcome {
	return Outcome{State: contract.RuntimeActive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}
}

func receiveCandidate(t *testing.T, channel <-chan Candidate) Candidate {
	t.Helper()
	select {
	case candidate := <-channel:
		return candidate
	case <-time.After(time.Second):
		t.Fatal("candidate did not start")
		return Candidate{}
	}
}

func TestManagerSerializesPerServerAndAdmitsFourGloballyWithoutWaiters(t *testing.T) {
	repository := newFakeRepository(5)
	driver := newBlockingDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()

	for id := range repository.servers {
		manager.Trigger(id, nil, false)
	}
	started := make(map[string]int)
	var coalescedID string
	for range 4 {
		candidate := receiveCandidate(t, driver.started)
		started[candidate.Server.ID]++
		if coalescedID == "" {
			coalescedID = candidate.Server.ID
		}
	}
	assert.True(t, manager.AdmissionStatus().Saturated)
	for range 8 {
		manager.Trigger(coalescedID, nil, false)
	}
	select {
	case candidate := <-driver.started:
		t.Fatalf("unexpected queued start: %s", candidate.Server.ID)
	default:
	}

	for range 2 {
		driver.release <- activeOutcome()
		candidate := receiveCandidate(t, driver.started)
		started[candidate.Server.ID]++
	}
	assert.Equal(t, 2, started[coalescedID])
	for serverID, count := range started {
		if serverID != coalescedID {
			assert.Equal(t, 1, count)
		}
	}
	for range 4 {
		driver.release <- activeOutcome()
	}
}

func TestManagerRejectsStalePublicationAndReconcilesLatestRevision(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newBlockingDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()
	var id string
	for id = range repository.servers {
		break
	}

	manager.Trigger(id, nil, true)
	first := receiveCandidate(t, driver.started)
	repository.setRevision(id, "2")
	manager.Trigger(id, nil, true)
	driver.release <- activeOutcome()
	cleaned := receiveCandidate(t, driver.cleaned)
	assert.Equal(t, first.RuntimeID, cleaned.RuntimeID)
	latest := receiveCandidate(t, driver.started)
	assert.Equal(t, "2", latest.Server.DesiredRevision)
	driver.release <- activeOutcome()
	require.Eventually(t, func() bool { return manager.Status(id).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	assert.Equal(t, latest.RuntimeID, *manager.Status(id).RuntimeID)
}

type fakeTimer struct {
	callback func()
	stopped  bool
}

func (timer *fakeTimer) Stop() bool { timer.stopped = true; return true }

type scheduledCall struct {
	delay time.Duration
	timer *fakeTimer
}
type fakeScheduler struct {
	mu     sync.Mutex
	calls  []scheduledCall
	notify chan struct{}
}

func newFakeScheduler() *fakeScheduler { return &fakeScheduler{notify: make(chan struct{}, 32)} }
func (scheduler *fakeScheduler) AfterFunc(delay time.Duration, callback func()) Timer {
	scheduler.mu.Lock()
	timer := &fakeTimer{callback: callback}
	scheduler.calls = append(scheduler.calls, scheduledCall{delay: delay, timer: timer})
	scheduler.mu.Unlock()
	scheduler.notify <- struct{}{}
	return timer
}
func (scheduler *fakeScheduler) fire(t *testing.T, index int) time.Duration {
	t.Helper()
	select {
	case <-scheduler.notify:
	case <-time.After(time.Second):
		t.Fatal("timer was not scheduled")
	}
	scheduler.mu.Lock()
	call := scheduler.calls[index]
	scheduler.mu.Unlock()
	require.False(t, call.timer.stopped)
	call.timer.callback()
	return call.delay
}

type outcomeDriver struct {
	mu       sync.Mutex
	outcomes []Outcome
	calls    chan Candidate
}

func (driver *outcomeDriver) Reconcile(_ context.Context, candidate Candidate) Outcome {
	driver.calls <- candidate
	driver.mu.Lock()
	defer driver.mu.Unlock()
	outcome := driver.outcomes[0]
	driver.outcomes = driver.outcomes[1:]
	return outcome
}
func (*outcomeDriver) Cleanup(context.Context, Candidate) {}

func TestManagerUsesExactRetryScheduleAndExplicitReset(t *testing.T) {
	reason := contract.ReasonConnectivity
	retry := Outcome{State: contract.RuntimeRetryWait, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason, Retryable: true}

	repository := newFakeRepository(1)
	driver := &outcomeDriver{outcomes: []Outcome{retry, retry, retry, retry, retry, retry, retry, retry, activeOutcome()}, calls: make(chan Candidate, 16)}
	scheduler := newFakeScheduler()
	manager, err := New(Options{Repository: repository, Driver: driver, Scheduler: scheduler})
	require.NoError(t, err)
	defer manager.Shutdown()
	var id string
	for id = range repository.servers {
		break
	}
	manager.Trigger(id, nil, true)

	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second}
	for index, expected := range delays {
		receiveCandidate(t, driver.calls)
		assert.Equal(t, expected, scheduler.fire(t, index))
	}
	receiveCandidate(t, driver.calls)

	resetRepository := newFakeRepository(1)
	resetDriver := &outcomeDriver{outcomes: []Outcome{retry, retry, retry, activeOutcome()}, calls: make(chan Candidate, 8)}
	resetScheduler := newFakeScheduler()
	resetManager, err := New(Options{Repository: resetRepository, Driver: resetDriver, Scheduler: resetScheduler})
	require.NoError(t, err)
	defer resetManager.Shutdown()
	var resetID string
	for resetID = range resetRepository.servers {
		break
	}
	resetManager.Trigger(resetID, nil, true)
	receiveCandidate(t, resetDriver.calls)
	assert.Equal(t, time.Second, resetScheduler.fire(t, 0))
	receiveCandidate(t, resetDriver.calls)
	select {
	case <-resetScheduler.notify:
	case <-time.After(time.Second):
		t.Fatal("second retry was not scheduled")
	}
	resetManager.Trigger(resetID, nil, true)
	receiveCandidate(t, resetDriver.calls)
	assert.Equal(t, time.Second, resetScheduler.fire(t, 2))
	receiveCandidate(t, resetDriver.calls)
}

func TestManagerDrainFencesLateDriverPublication(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newBlockingDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	var id string
	for id = range repository.servers {
		break
	}
	manager.Trigger(id, nil, false)
	candidate := receiveCandidate(t, driver.started)
	manager.Shutdown()
	cleaned := receiveCandidate(t, driver.cleaned)
	assert.Equal(t, candidate.RuntimeID, cleaned.RuntimeID)
	assert.NotEqual(t, contract.RuntimeActive, manager.Status(id).State)
}

func TestManagerStartupInterruptsAndReconstructsOnlyEnabledServers(t *testing.T) {
	repository := newFakeRepository(2)
	var disabledID string
	for id, server := range repository.servers {
		disabledID = id
		server.DesiredState = contract.DesiredServerDisabled
		repository.servers[id] = server
		break
	}
	driver := newBlockingDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()
	require.NoError(t, manager.Start(context.Background()))
	assert.True(t, repository.interrupted)
	candidate := receiveCandidate(t, driver.started)
	assert.NotEqual(t, disabledID, candidate.Server.ID)
	assert.Equal(t, contract.RuntimeInactive, manager.Status(disabledID).State)
	driver.release <- activeOutcome()
}
