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
	transitions chan servers.Operation
}

func newFakeRepository(count int) *fakeRepository {
	repository := &fakeRepository{servers: make(map[string]servers.Server), authorities: make(map[string]servers.AuthorityMetadata), operations: make(map[string]servers.Operation), transitions: make(chan servers.Operation, 64)}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G5F%02d", index)
		repository.servers[id] = servers.Server{ID: id, Namespace: fmt.Sprintf("server-%d", index), DisplayName: "Server", DesiredState: contract.DesiredServerEnabled, DesiredRevision: "1", Transport: []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`), CreatedAt: "2026-08-23T00:00:00Z", UpdatedAt: "2026-08-23T00:00:00Z"}
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
	repository.transitions <- operation
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
func (repository *fakeRepository) setDesiredState(id string, state contract.DesiredServerState) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	server := repository.servers[id]
	server.DesiredState = state
	repository.servers[id] = server
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
func (driver *blockingDriver) Reconcile(ctx context.Context, candidate Candidate, _ *MaterialLease) Outcome {
	driver.started <- candidate
	select {
	case outcome := <-driver.release:
		return outcome
	case <-ctx.Done():
		return Outcome{}
	}
}
func (driver *blockingDriver) Stop(_ context.Context, candidate Candidate) bool {
	driver.cleaned <- candidate
	return true
}

type recordingAuthority struct{ called chan Candidate }

func (authority *recordingAuthority) Resolve(_ context.Context, candidate Candidate) AuthorityOutcome {
	authority.called <- candidate
	return AuthorityOutcome{CredentialState: contract.ServerCredentialNotRequired}
}

type immediateDriver struct{ called chan Candidate }

func (driver *immediateDriver) Reconcile(_ context.Context, candidate Candidate, _ *MaterialLease) Outcome {
	driver.called <- candidate
	return activeOutcome()
}
func (*immediateDriver) Stop(context.Context, Candidate) bool { return true }

type leasingAuthority struct{}

func (leasingAuthority) Resolve(_ context.Context, candidate Candidate) AuthorityOutcome {
	lease, _ := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: []byte("manager-canary")})
	return AuthorityOutcome{CredentialState: contract.ServerCredentialReady, Lease: lease}
}

type ownerDriver struct {
	owner    *RuntimeOwner
	admitted chan Candidate
}

func (driver *ownerDriver) Reconcile(_ context.Context, candidate Candidate, lease *MaterialLease) Outcome {
	_, err := driver.owner.Admit(candidate, lease, nil)
	if err != nil {
		return Outcome{State: contract.RuntimeDegraded}
	}
	driver.admitted <- candidate
	return activeOutcome()
}

func (driver *ownerDriver) Stop(_ context.Context, candidate Candidate) bool {
	return driver.owner.Release(candidate.Key(), true)
}

func TestManagerTransfersAuthorityLeaseToDriver(t *testing.T) {
	repository := newFakeRepository(1)
	var serverID string
	for id, server := range repository.servers {
		serverID = id
		server.Transport = []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api"}}`)
		repository.servers[id] = server
	}
	driver := &ownerDriver{owner: NewRuntimeOwner(), admitted: make(chan Candidate, 1)}
	manager, err := New(Options{Repository: repository, Authority: leasingAuthority{}, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()

	manager.Trigger(serverID, nil, false)
	candidate := receiveCandidate(t, driver.admitted)
	material, ok := driver.owner.Material(candidate.Key(), contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Equal(t, "manager-canary", string(material))
}

type stalingLeaseAuthority struct {
	manager  *Manager
	lease    *MaterialLease
	key      CandidateKey
	resolved chan struct{}
	stale    func(*Manager, string)
}

func (authority *stalingLeaseAuthority) Resolve(_ context.Context, candidate Candidate) AuthorityOutcome {
	authority.key = candidate.Key()
	authority.lease, _ = NewMaterialLease(authority.key, map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: []byte("stale-canary")})
	authority.stale(authority.manager, candidate.Server.ID)
	close(authority.resolved)
	return AuthorityOutcome{CredentialState: contract.ServerCredentialReady, Lease: authority.lease}
}

func TestManagerClearsLeaseWhenCandidateStalesBeforeDriver(t *testing.T) {
	tests := []struct {
		name  string
		stale func(*Manager, string)
	}{
		{name: "generation", stale: func(manager *Manager, serverID string) { manager.Fence(serverID) }},
		{name: "drain", stale: func(manager *Manager, _ string) { manager.Drain(context.Background()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository(1)
			var serverID string
			for id, server := range repository.servers {
				serverID = id
				server.Transport = []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api"}}`)
				repository.servers[id] = server
			}
			authority := &stalingLeaseAuthority{resolved: make(chan struct{}), stale: test.stale}
			driver := &immediateDriver{called: make(chan Candidate, 1)}
			manager, err := New(Options{Repository: repository, Authority: authority, Driver: driver})
			require.NoError(t, err)
			authority.manager = manager
			defer manager.Shutdown()

			manager.Trigger(serverID, nil, false)
			<-authority.resolved

			require.Eventually(t, func() bool { return manager.AdmissionStatus().InUse == 0 }, time.Second, time.Millisecond)
			select {
			case <-driver.called:
				t.Fatal("driver received material for a stale candidate")
			default:
			}
			_, transferred := authority.lease.transfer(authority.key)
			assert.False(t, transferred)
		})
	}
}

func TestManagerRejectsInvalidPersistedTransportBeforeExternalWork(t *testing.T) {
	repository := newFakeRepository(1)
	var serverID string
	for id, server := range repository.servers {
		serverID = id
		server.Transport = []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{},"unknown":true}`)
		repository.servers[id] = server
	}
	authority := &recordingAuthority{called: make(chan Candidate, 1)}
	driver := &immediateDriver{called: make(chan Candidate, 1)}
	manager, err := New(Options{Repository: repository, Authority: authority, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)

	manager.Trigger(serverID, &operation.Operation.ID, true)

	var terminal servers.Operation
	for range 2 {
		select {
		case transitioned := <-repository.transitions:
			terminal = transitioned
		case <-time.After(time.Second):
			t.Fatal("operation transition was not received")
		}
	}
	assert.Equal(t, contract.OperationFailed, terminal.State)
	require.NotNil(t, terminal.Reason)
	assert.Equal(t, contract.ReasonConfigurationInvalid, *terminal.Reason)
	select {
	case <-authority.called:
		t.Fatal("credential authority was consulted for invalid persisted transport")
	default:
	}
	select {
	case <-driver.called:
		t.Fatal("runtime driver was called for invalid persisted transport")
	default:
	}
}

type contextStopDriver struct {
	stopping chan Candidate
}

func (driver *contextStopDriver) Reconcile(context.Context, Candidate, *MaterialLease) Outcome {
	return activeOutcome()
}
func (driver *contextStopDriver) Stop(ctx context.Context, candidate Candidate) bool {
	driver.stopping <- candidate
	<-ctx.Done()
	return false
}

type lifecycleDriver struct {
	started     chan Candidate
	startResult chan Outcome
	stopping    chan Candidate
	stopResult  chan bool
}

func newLifecycleDriver() *lifecycleDriver {
	return &lifecycleDriver{started: make(chan Candidate, 16), startResult: make(chan Outcome, 16), stopping: make(chan Candidate, 16), stopResult: make(chan bool, 16)}
}

func (driver *lifecycleDriver) Reconcile(_ context.Context, candidate Candidate, _ *MaterialLease) Outcome {
	driver.started <- candidate
	return <-driver.startResult
}

func (driver *lifecycleDriver) Stop(_ context.Context, candidate Candidate) bool {
	driver.stopping <- candidate
	return <-driver.stopResult
}

func TestCredentialCutoverFenceAdvancesWithoutIndependentActiveState(t *testing.T) {
	serverID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	publisher := newMemoryPublisher()
	candidate := Candidate{Server: servers.Server{ID: serverID}, RuntimeID: "runtime-before-replacement", Generation: 1}
	publisher.Fence(serverID, 1)
	manager := &Manager{publisher: publisher, entries: map[string]*entry{serverID: {generation: 1, active: &candidate}}}

	manager.Fence(serverID)

	publisher.mu.Lock()
	generation := publisher.fences[serverID]
	publisher.mu.Unlock()
	assert.Equal(t, uint64(2), generation)
	assert.Equal(t, "runtime-before-replacement", manager.entries[serverID].active.RuntimeID)
}

type publisherEvent struct {
	step      string
	candidate Candidate
}

type recordingPublisher struct {
	delegate *memoryPublisher
	events   chan publisherEvent
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{delegate: newMemoryPublisher(), events: make(chan publisherEvent, 32)}
}

func (publisher *recordingPublisher) Fence(serverID string, generation uint64) {
	publisher.delegate.Fence(serverID, generation)
	publisher.events <- publisherEvent{step: "fence", candidate: Candidate{Server: servers.Server{ID: serverID}, Generation: generation}}
}

func (publisher *recordingPublisher) Withdraw(candidate Candidate) {
	publisher.delegate.Withdraw(candidate)
	publisher.events <- publisherEvent{step: "withdraw", candidate: candidate}
}

func receivePublisherEvent(t *testing.T, events <-chan publisherEvent) publisherEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("publisher event was not received")
		return publisherEvent{}
	}
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

func TestManagerCurrentRequiresExactActivatingCandidate(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newBlockingDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for id, server := range repository.servers {
		serverID = id
		server.Transport = []byte(`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api"}}`)
		repository.servers[id] = server
	}
	manager.Trigger(serverID, nil, false)
	candidate := receiveCandidate(t, driver.started)
	require.True(t, manager.Current(candidate))

	mismatches := []Candidate{
		candidate,
		candidate,
		candidate,
		candidate,
		candidate,
		candidate,
		candidate,
		candidate,
	}
	mismatches[0].Server.ID = "different-server"
	mismatches[1].Server.DesiredRevision = "2"
	mismatches[2].Server.Transport = append([]byte(nil), candidate.Server.Transport...)
	mismatches[2].Server.Transport[1] = 'X'
	mismatches[3].Server.DesiredState = contract.DesiredServerDisabled
	mismatches[4].Authority.CredentialRevisions.StaticCredential = "1"
	mismatches[5].RuntimeID = "different-runtime"
	mismatches[6].Generation++
	mismatches[7].DrainEpoch++
	for _, mismatch := range mismatches {
		assert.False(t, manager.Current(mismatch))
	}
	driver.release <- activeOutcome()
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

func establishActiveRuntime(t *testing.T, manager *Manager, driver *lifecycleDriver, publisher *recordingPublisher, serverID string) Candidate {
	t.Helper()
	manager.Trigger(serverID, nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	candidate := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	return candidate
}

type refreshCatalog struct {
	refreshStarted chan Candidate
	release        chan CatalogOutcome
	withdrawn      chan Candidate
}

func newRefreshCatalog() *refreshCatalog {
	return &refreshCatalog{refreshStarted: make(chan Candidate, 4), release: make(chan CatalogOutcome, 4), withdrawn: make(chan Candidate, 4)}
}
func (catalog *refreshCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	return CatalogOutcome{State: contract.ActiveCatalogCurrent}
}
func (catalog *refreshCatalog) Refresh(_ context.Context, candidate Candidate) CatalogOutcome {
	catalog.refreshStarted <- candidate
	return <-catalog.release
}
func (catalog *refreshCatalog) Withdraw(candidate Candidate, _ contract.ActiveCatalogState) {
	catalog.withdrawn <- candidate
}

func TestManagerRefreshCatalogOperationSkipsLifecycleAndCompletesAttachedWork(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	catalog := newRefreshCatalog()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Catalog: catalog})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, serverID)
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.Trigger(serverID, &operation.Operation.ID, false)
	assert.Equal(t, active.RuntimeID, receiveCandidate(t, catalog.refreshStarted).RuntimeID)
	assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
	select {
	case event := <-publisher.events:
		t.Fatalf("refresh unexpectedly changed runtime publication: %s", event.step)
	default:
	}
	catalog.release <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	terminal := <-repository.transitions
	assert.Equal(t, contract.OperationSucceeded, terminal.State)
	assert.Equal(t, contract.RuntimeActive, manager.Status(serverID).State)
	assert.Equal(t, contract.ActiveCatalogCurrent, manager.Status(serverID).CatalogState)
	select {
	case <-driver.stopping:
		t.Fatal("refresh unexpectedly stopped the runtime")
	default:
	}
}

func TestManagerWithdrawsAndVerifiesStopBeforeReplacementPublicationAndSuccess(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, serverID)
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationReload, ExpectedDesiredRevision: "2"})
	require.NoError(t, err)
	repository.setRevision(serverID, "2")
	manager.Trigger(serverID, &operation.Operation.ID, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	running := <-repository.transitions
	assert.Equal(t, contract.OperationRunning, running.State)
	withdraw := receivePublisherEvent(t, publisher.events)
	assert.Equal(t, "withdraw", withdraw.step)
	stopping := receiveCandidate(t, driver.stopping)
	assert.Equal(t, active.RuntimeID, stopping.RuntimeID)
	select {
	case candidate := <-driver.started:
		t.Fatalf("replacement started before verified stop: %s", candidate.RuntimeID)
	default:
	}
	driver.stopResult <- true
	replacement := receiveCandidate(t, driver.started)
	assert.NotEqual(t, active.RuntimeID, replacement.RuntimeID)
	driver.startResult <- activeOutcome()
	succeeded := <-repository.transitions
	assert.Equal(t, contract.OperationSucceeded, succeeded.State)
	assert.Equal(t, replacement.RuntimeID, *manager.Status(serverID).RuntimeID)
}

func TestSupersededCandidateWithUnconfirmedStopCannotStartLatestReplacement(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	manager.Trigger(serverID, nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	candidate := receiveCandidate(t, driver.started)
	repository.setRevision(serverID, "2")
	manager.Trigger(serverID, nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	driver.startResult <- activeOutcome()
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- false
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- false
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
	}, time.Second, time.Millisecond)
	select {
	case replacement := <-driver.started:
		t.Fatalf("latest replacement started without candidate cleanup: %s", replacement.RuntimeID)
	default:
	}
}

func TestManagerStopUnconfirmedWithdrawsAndStartsNoReplacement(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	_ = establishActiveRuntime(t, manager, driver, publisher, serverID)
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationReload, ExpectedDesiredRevision: "2"})
	require.NoError(t, err)
	repository.setRevision(serverID, "2")
	manager.Trigger(serverID, &operation.Operation.ID, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	_ = receiveCandidate(t, driver.stopping)
	driver.stopResult <- false
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
	}, time.Second, time.Millisecond)
	assert.Equal(t, int64(1), manager.RuntimeStatus().InUse)
	failed := <-repository.transitions
	assert.Equal(t, contract.OperationFailed, failed.State)
	assert.Equal(t, contract.ReasonStopUnconfirmed, *failed.Reason)
	select {
	case candidate := <-driver.started:
		t.Fatalf("replacement started after unconfirmed stop: %s", candidate.RuntimeID)
	default:
	}
}

func TestDisabledStopUnconfirmedRetryPerformsCleanupOnly(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	_ = establishActiveRuntime(t, manager, driver, publisher, serverID)
	repository.setDesiredState(serverID, contract.DesiredServerDisabled)
	manager.Trigger(serverID, nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	_ = receiveCandidate(t, driver.stopping)
	driver.stopResult <- false
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeDegraded }, time.Second, time.Millisecond)

	manager.Trigger(serverID, nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	_ = receiveCandidate(t, driver.stopping)
	driver.stopResult <- true
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeInactive }, time.Second, time.Millisecond)
	select {
	case candidate := <-driver.started:
		t.Fatalf("cleanup retry started a disabled runtime: %s", candidate.RuntimeID)
	default:
	}
}

func TestManagerDisableAndDeleteStopWithoutReplacement(t *testing.T) {
	for _, test := range []struct {
		name    string
		desired contract.DesiredServerState
		want    contract.RuntimeState
	}{
		{name: "disabled", desired: contract.DesiredServerDisabled, want: contract.RuntimeInactive},
		{name: "deleted", desired: contract.DesiredServerDeleted, want: contract.RuntimeDeleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := newLifecycleDriver()
			publisher := newRecordingPublisher()
			manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
			require.NoError(t, err)
			defer manager.Shutdown()
			var serverID string
			for serverID = range repository.servers {
				break
			}
			_ = establishActiveRuntime(t, manager, driver, publisher, serverID)
			repository.setDesiredState(serverID, test.desired)
			manager.Trigger(serverID, nil, true)
			assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
			assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
			_ = receiveCandidate(t, driver.stopping)
			driver.stopResult <- true
			require.Eventually(t, func() bool { return manager.Status(serverID).State == test.want }, time.Second, time.Millisecond)
			select {
			case candidate := <-driver.started:
				t.Fatalf("runtime started for %s server: %s", test.desired, candidate.RuntimeID)
			default:
			}
		})
	}
}

func TestManagerDisplayOnlyRevisionChangePreservesActiveRuntime(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, serverID)
	repository.setRevision(serverID, "2")
	assert.Equal(t, active.RuntimeID, *manager.Status(serverID).RuntimeID)
	select {
	case event := <-publisher.events:
		t.Fatalf("display-only change affected publication: %s", event.step)
	case candidate := <-driver.stopping:
		t.Fatalf("display-only change stopped runtime: %s", candidate.RuntimeID)
	default:
	}
}

func TestBlockedStopDoesNotBlockUnrelatedServerActivation(t *testing.T) {
	repository := newFakeRepository(2)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	defer manager.Shutdown()
	ids := make([]string, 0, 2)
	for serverID := range repository.servers {
		ids = append(ids, serverID)
	}
	_ = establishActiveRuntime(t, manager, driver, publisher, ids[0])
	repository.setRevision(ids[0], "2")
	manager.Trigger(ids[0], nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	_ = receiveCandidate(t, driver.stopping)
	manager.Trigger(ids[1], nil, true)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	other := receiveCandidate(t, driver.started)
	assert.Equal(t, ids[1], other.Server.ID)
	driver.startResult <- activeOutcome()
	require.Eventually(t, func() bool { return manager.Status(ids[1]).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	driver.stopResult <- true
}

func TestRuntimeFailureWithdrawsBeforeRetryAndRejectsStaleFailure(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	scheduler := newFakeScheduler()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, serverID)
	stale := active
	stale.Generation++
	assert.False(t, manager.RuntimeFailed(stale, contract.ReasonProcessExited))
	results := make(chan bool, 32)
	var reports sync.WaitGroup
	for range 32 {
		reports.Add(1)
		go func() {
			defer reports.Done()
			results <- manager.RuntimeFailed(active, contract.ReasonProcessExited)
		}()
	}
	reports.Wait()
	close(results)
	accepted := 0
	for result := range results {
		if result {
			accepted++
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	stopping := receiveCandidate(t, driver.stopping)
	assert.Equal(t, active.RuntimeID, stopping.RuntimeID)
	driver.stopResult <- true
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeRetryWait && status.Reason != nil && *status.Reason == contract.ReasonProcessExited && status.CatalogState == contract.ActiveCatalogUnavailable
	}, time.Second, time.Millisecond)
	assert.Equal(t, time.Second, scheduler.fire(t, 0))
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	replacement := receiveCandidate(t, driver.started)
	assert.NotEqual(t, active.RuntimeID, replacement.RuntimeID)
	driver.startResult <- activeOutcome()
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	assert.False(t, manager.RuntimeFailed(active, contract.ReasonProcessExited))
	assert.Equal(t, replacement.RuntimeID, *manager.Status(serverID).RuntimeID)
}

func TestRuntimeFailureDuringActivationUsesExactNonretryableDisposition(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	scheduler := newFakeScheduler()
	manager, err := New(Options{Repository: repository, Driver: driver, Scheduler: scheduler})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	manager.Trigger(serverID, nil, true)
	candidate := receiveCandidate(t, driver.started)
	assert.False(t, manager.ReportRuntimeFailure(candidate, FailureDisposition{RuntimeLost: true}))
	failure := FailureDisposition{State: contract.RuntimeAuthenticationRequired, Reason: contract.ReasonAuthenticationRejected, RuntimeLost: true}
	assert.True(t, manager.ReportRuntimeFailure(candidate, failure))
	assert.False(t, manager.ReportRuntimeFailure(candidate, failure))
	driver.startResult <- activeOutcome()
	stopping := receiveCandidate(t, driver.stopping)
	assert.Equal(t, candidate.Key(), stopping.Key())
	driver.stopResult <- true
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeAuthenticationRequired && status.Reason != nil && *status.Reason == contract.ReasonAuthenticationRejected && status.CredentialState == contract.ServerCredentialUnavailable && status.CatalogState == contract.ActiveCatalogUnavailable
	}, time.Second, time.Millisecond)
	select {
	case <-scheduler.notify:
		t.Fatal("nonretryable runtime failure scheduled a retry")
	default:
	}
}

func TestRuntimeFailureStopUnconfirmedNeverRetries(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	scheduler := newFakeScheduler()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, serverID)
	require.True(t, manager.ReportRuntimeFailure(active, FailureDisposition{State: contract.RuntimeDegraded, Reason: contract.ReasonProcessExited, Retryable: true, RuntimeLost: true}))
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, active.Key(), receiveCandidate(t, driver.stopping).Key())
	driver.stopResult <- false
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
	}, time.Second, time.Millisecond)
	select {
	case <-scheduler.notify:
		t.Fatal("unconfirmed stop scheduled a retry")
	default:
	}
	select {
	case replacement := <-driver.started:
		t.Fatalf("unconfirmed stop started replacement %s", replacement.RuntimeID)
	default:
	}
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

func (driver *outcomeDriver) Reconcile(_ context.Context, candidate Candidate, _ *MaterialLease) Outcome {
	driver.calls <- candidate
	driver.mu.Lock()
	defer driver.mu.Unlock()
	outcome := driver.outcomes[0]
	driver.outcomes = driver.outcomes[1:]
	return outcome
}
func (*outcomeDriver) Stop(context.Context, Candidate) bool { return true }

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

func TestManagerPublishesSafeInvalidationsAfterTransitions(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newBlockingDriver()
	invalidations := make(chan contract.Invalidation, 16)
	manager, err := New(Options{Repository: repository, Driver: driver, Invalidate: func(invalidation contract.Invalidation) { invalidations <- invalidation }})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	created, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.Trigger(serverID, &created.Operation.ID, false)
	_ = receiveCandidate(t, driver.started)
	driver.release <- activeOutcome()

	seen := make(map[contract.InvalidationKind]bool)
	deadline := time.After(time.Second)
	for !seen[contract.InvalidationServers] || !seen[contract.InvalidationServerOperations] || !seen[contract.InvalidationSystemStatus] {
		select {
		case invalidation := <-invalidations:
			seen[invalidation.Kind] = true
			if invalidation.ResourceID != nil {
				assert.NotContains(t, *invalidation.ResourceID, "connectivity")
			}
		case <-deadline:
			t.Fatalf("missing invalidations: %#v", seen)
		}
	}
	require.Eventually(t, func() bool {
		operation, getErr := repository.GetOperation(context.Background(), created.Operation.ID)
		return getErr == nil && operation.State == contract.OperationSucceeded
	}, time.Second, time.Millisecond)
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

func TestManagerDrainWithdrawsAllAndStopsConcurrentlyOutsideAdmission(t *testing.T) {
	repository := newFakeRepository(4)
	driver := newLifecycleDriver()
	publisher := newRecordingPublisher()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher})
	require.NoError(t, err)
	manager.mu.Lock()
	manager.globalInUse = manager.globalLimit
	for serverID, server := range repository.servers {
		current := manager.entryLocked(serverID)
		current.generation = 1
		candidate := Candidate{Server: server, RuntimeID: "runtime-" + serverID, Generation: 1}
		current.active = &candidate
		current.status.State = contract.RuntimeActive
		current.status.RuntimeID = &candidate.RuntimeID
		publisher.delegate.Fence(serverID, candidate.Generation)
	}
	manager.mu.Unlock()

	done := manager.Drain(context.Background())
	for range 4 {
		assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
		assert.Equal(t, "withdraw", receivePublisherEvent(t, publisher.events).step)
	}
	stopping := make(map[string]bool)
	for range 4 {
		stopping[receiveCandidate(t, driver.stopping).RuntimeID] = true
	}
	assert.Len(t, stopping, 4)
	for range 3 {
		driver.stopResult <- true
	}
	driver.stopResult <- false
	result := <-done
	assert.Equal(t, DrainResult{Verified: 3, Unconfirmed: 1}, result)
	assert.Zero(t, manager.RuntimeStatus().InUse)
}

func TestManagerDrainDeadlineClassifiesUnconfirmedWithoutExtending(t *testing.T) {
	repository := newFakeRepository(1)
	driver := &contextStopDriver{stopping: make(chan Candidate, 1)}
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	var serverID string
	var server servers.Server
	for serverID, server = range repository.servers {
		break
	}
	candidate := Candidate{Server: server, RuntimeID: "runtime-deadline", Generation: 1}
	manager.mu.Lock()
	current := manager.entryLocked(serverID)
	current.active = &candidate
	current.generation = 1
	manager.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := manager.Drain(ctx)
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	cancel()
	assert.Equal(t, DrainResult{Unconfirmed: 1}, <-done)
}

func TestManagerDrainRejectsLateCredentialStatus(t *testing.T) {
	manager, err := New(Options{Repository: newFakeRepository(0)})
	require.NoError(t, err)
	manager.SetCredentialState("server", contract.ServerCredentialRefreshing, true)
	<-manager.Drain(context.Background())
	manager.SetCredentialState("server", contract.ServerCredentialReady, false)
	assert.Equal(t, contract.ServerCredentialRefreshing, manager.Status("server").CredentialState)
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
