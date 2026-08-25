package runtimes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	mu               sync.Mutex
	servers          map[string]servers.Server
	authorities      map[string]servers.AuthorityMetadata
	operations       map[string]servers.Operation
	nextID           int
	interrupted      bool
	transitions      chan servers.Operation
	beforeTransition func(contract.ServerOperationState) error
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
	hook := repository.beforeTransition
	repository.mu.Unlock()
	if hook != nil {
		if err := hook(state); err != nil {
			return servers.Operation{}, err
		}
	}
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

type outcomeCatalog struct{ outcome CatalogOutcome }

func (catalog outcomeCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	return catalog.outcome
}

type durableOnlyCatalog struct {
	outcome   CatalogOutcome
	onPublish func()
	abandoned chan Candidate
}

func (catalog *durableOnlyCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	if catalog.onPublish != nil {
		catalog.onPublish()
	}
	return catalog.outcome
}

func (catalog *durableOnlyCatalog) Abandon(candidate Candidate) {
	catalog.abandoned <- candidate
}

type finalizationCatalog struct {
	mu        sync.Mutex
	routable  bool
	installed chan Candidate
	withdrawn chan Candidate
}

func newFinalizationCatalog() *finalizationCatalog {
	return &finalizationCatalog{installed: make(chan Candidate, 1), withdrawn: make(chan Candidate, 1)}
}

func (catalog *finalizationCatalog) Activate(_ context.Context, candidate Candidate) CatalogOutcome {
	catalog.mu.Lock()
	catalog.routable = true
	catalog.mu.Unlock()
	catalog.installed <- candidate
	return CatalogOutcome{State: contract.ActiveCatalogCurrent}
}

func (catalog *finalizationCatalog) Refresh(ctx context.Context, candidate Candidate) CatalogOutcome {
	return catalog.Activate(ctx, candidate)
}

func (catalog *finalizationCatalog) Withdraw(candidate Candidate, _ contract.ActiveCatalogState) {
	catalog.mu.Lock()
	catalog.routable = false
	catalog.mu.Unlock()
	catalog.withdrawn <- candidate
}

func (catalog *finalizationCatalog) routeVisible() bool {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.routable
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

type handoffCatalog struct {
	activateStarted chan Candidate
	activateResult  chan CatalogOutcome
	refreshStarted  chan Candidate
	refreshResult   chan CatalogOutcome
	withdrawn       chan Candidate
}

func newHandoffCatalog() *handoffCatalog {
	return &handoffCatalog{activateStarted: make(chan Candidate, 4), activateResult: make(chan CatalogOutcome, 4), refreshStarted: make(chan Candidate, 4), refreshResult: make(chan CatalogOutcome, 4), withdrawn: make(chan Candidate, 4)}
}

func (catalog *handoffCatalog) Activate(_ context.Context, candidate Candidate) CatalogOutcome {
	catalog.activateStarted <- candidate
	return <-catalog.activateResult
}
func (catalog *handoffCatalog) Refresh(_ context.Context, candidate Candidate) CatalogOutcome {
	catalog.refreshStarted <- candidate
	return <-catalog.refreshResult
}
func (catalog *handoffCatalog) Withdraw(candidate Candidate, _ contract.ActiveCatalogState) {
	catalog.withdrawn <- candidate
}

func TestManagerExplicitCatalogChallengeKeepsOperationAttachedThroughFreshTraversal(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	catalog := newHandoffCatalog()
	refresh := newChallengeRefreshFake()
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, OAuthRefresh: refresh})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	repository.mu.Lock()
	authority := repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "1"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	manager.Trigger(serverID, nil, false)
	active := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	assert.Equal(t, active.RuntimeID, receiveCandidate(t, catalog.activateStarted).RuntimeID)
	catalog.activateResult <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)

	manager.Trigger(serverID, &operation.Operation.ID, false)
	refreshCandidate := receiveCandidate(t, catalog.refreshStarted)
	require.NotNil(t, refreshCandidate.OperationID)
	assert.Equal(t, operation.Operation.ID, *refreshCandidate.OperationID)
	assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeCatalogFirstPage}
	catalog.refreshResult <- CatalogOutcome{State: contract.ActiveCatalogCurrent, OAuthChallenge: disposition}
	<-refresh.started
	select {
	case terminal := <-repository.transitions:
		t.Fatalf("operation terminated before fresh traversal: %s", terminal.State)
	default:
	}
	repository.mu.Lock()
	authority = repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "2"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "2"}
	assert.Equal(t, active.RuntimeID, receiveCandidate(t, catalog.withdrawn).RuntimeID)
	assert.Equal(t, active.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	fresh := receiveCandidate(t, driver.started)
	assert.Equal(t, downstream.OAuthChallengeCatalogFirstPage, fresh.OAuthReplayStage)
	require.NotNil(t, fresh.OperationID)
	assert.Equal(t, operation.Operation.ID, *fresh.OperationID)
	driver.startResult <- activeOutcome()
	assert.Equal(t, fresh.RuntimeID, receiveCandidate(t, catalog.activateStarted).RuntimeID)
	catalog.activateResult <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	terminal := <-repository.transitions
	assert.Equal(t, contract.OperationSucceeded, terminal.State)
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeActive && status.RuntimeID != nil && *status.RuntimeID == fresh.RuntimeID
	}, time.Second, time.Millisecond)
	select {
	case <-refresh.started:
		t.Fatal("attached operation caused a second OAuth refresh")
	default:
	}
}

func TestManagerPollCatalogChallengeUsesSameHandoffWithoutOperation(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	catalog := newHandoffCatalog()
	refresh := newChallengeRefreshFake()
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, OAuthRefresh: refresh})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	repository.mu.Lock()
	authority := repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "1"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	manager.Trigger(serverID, nil, false)
	active := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	<-catalog.activateStarted
	catalog.activateResult <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeCatalogFirstPage}

	assert.True(t, manager.HandleCatalogCompletion(active, CatalogOutcome{State: contract.ActiveCatalogCurrent, OAuthChallenge: disposition}, nil))
	assert.True(t, manager.HandleCatalogCompletion(active, CatalogOutcome{State: contract.ActiveCatalogCurrent, OAuthChallenge: disposition}, nil))
	<-refresh.started
	repository.mu.Lock()
	authority = repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "2"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "2"}
	<-catalog.withdrawn
	assert.Equal(t, active.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	fresh := receiveCandidate(t, driver.started)
	assert.Nil(t, fresh.OperationID)
	driver.startResult <- activeOutcome()
	<-catalog.activateStarted
	catalog.activateResult <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
	select {
	case transition := <-repository.transitions:
		t.Fatalf("poll created an operation transition: %s", transition.State)
	default:
	}
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

type challengeRefreshFake struct {
	started chan OAuthChallengeRefreshRequest
	release chan OAuthChallengeRefreshResult
	errors  chan error
}

func newChallengeRefreshFake() *challengeRefreshFake {
	return &challengeRefreshFake{started: make(chan OAuthChallengeRefreshRequest, 4), release: make(chan OAuthChallengeRefreshResult, 4), errors: make(chan error, 4)}
}

func (refresh *challengeRefreshFake) RefreshOAuthChallenge(_ context.Context, request OAuthChallengeRefreshRequest) (OAuthChallengeRefreshResult, error) {
	refresh.started <- request
	select {
	case err := <-refresh.errors:
		return OAuthChallengeRefreshResult{}, err
	case result := <-refresh.release:
		return result, nil
	}
}

type sequenceCatalog struct {
	started  chan Candidate
	outcomes chan CatalogOutcome
}

func newSequenceCatalog() *sequenceCatalog {
	return &sequenceCatalog{started: make(chan Candidate, 8), outcomes: make(chan CatalogOutcome, 8)}
}

func (catalog *sequenceCatalog) Activate(_ context.Context, candidate Candidate) CatalogOutcome {
	catalog.started <- candidate
	return <-catalog.outcomes
}

func TestManagerOAuthChallengeRefreshUsesFreshCandidateAndExactReplayStage(t *testing.T) {
	stages := []downstream.OAuthChallengeStage{downstream.OAuthChallengeModernDiscovery, downstream.OAuthChallengeLegacyInitialize, downstream.OAuthChallengeCatalogFirstPage}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := newLifecycleDriver()
			catalog := newSequenceCatalog()
			refresh := newChallengeRefreshFake()
			authorityResolver := &recordingAuthority{called: make(chan Candidate, 4)}
			manager, err := New(Options{Repository: repository, Driver: driver, Authority: authorityResolver, Catalog: catalog, OAuthRefresh: refresh})
			require.NoError(t, err)
			defer manager.Shutdown()
			var serverID string
			for serverID = range repository.servers {
				break
			}
			repository.mu.Lock()
			authority := repository.authorities[serverID]
			authority.RegistrationRevision = "3"
			authority.CredentialRevisions.OAuthClient = "4"
			authority.CredentialRevisions.OAuthTokens = "5"
			repository.authorities[serverID] = authority
			repository.mu.Unlock()
			disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: stage, Metadata: []string{"https://resource.example/metadata"}}

			manager.Trigger(serverID, nil, false)
			oldCandidate := receiveCandidate(t, driver.started)
			assert.Equal(t, oldCandidate.RuntimeID, receiveCandidate(t, authorityResolver.called).RuntimeID)
			if stage == downstream.OAuthChallengeCatalogFirstPage {
				driver.startResult <- activeOutcome()
				assert.Equal(t, oldCandidate.RuntimeID, receiveCandidate(t, catalog.started).RuntimeID)
				catalog.outcomes <- CatalogOutcome{State: contract.ActiveCatalogUnavailable, OAuthChallenge: disposition}
			} else {
				driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, CredentialState: contract.ServerCredentialUnavailable, CatalogState: contract.ActiveCatalogAbsent, OAuthChallenge: disposition}
			}
			request := <-refresh.started
			assert.Equal(t, serverID, request.ServerID)
			assert.Equal(t, "1", request.ExpectedDesiredRevision)
			assert.Equal(t, "3", request.ExpectedRegistrationRevision)
			assert.Equal(t, "4", request.ExpectedOAuthClientRevision)
			assert.Equal(t, "5", request.ExpectedOAuthTokensRevision)
			assert.Equal(t, disposition.Metadata, request.ChallengeMetadata)
			repository.mu.Lock()
			authority = repository.authorities[serverID]
			authority.CredentialRevisions.OAuthTokens = "6"
			repository.authorities[serverID] = authority
			repository.mu.Unlock()
			refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "6"}
			assert.Equal(t, oldCandidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
			driver.stopResult <- true
			fresh := receiveCandidate(t, driver.started)
			assert.Equal(t, fresh.RuntimeID, receiveCandidate(t, authorityResolver.called).RuntimeID)
			assert.NotEqual(t, oldCandidate.RuntimeID, fresh.RuntimeID)
			assert.Equal(t, "6", fresh.Authority.CredentialRevisions.OAuthTokens)
			assert.Equal(t, stage, fresh.OAuthReplayStage)
			driver.startResult <- activeOutcome()
			assert.Equal(t, fresh.RuntimeID, receiveCandidate(t, catalog.started).RuntimeID)
			catalog.outcomes <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
			require.Eventually(t, func() bool {
				status := manager.Status(serverID)
				return status.State == contract.RuntimeActive && status.RuntimeID != nil && *status.RuntimeID == fresh.RuntimeID
			}, time.Second, time.Millisecond)
			select {
			case <-refresh.started:
				t.Fatal("OAuth challenge refreshed more than once")
			default:
			}
		})
	}
}

func TestManagerOAuthChallengeRefreshFailureStopsWithoutReplacement(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	refresh := newChallengeRefreshFake()
	manager, err := New(Options{Repository: repository, Driver: driver, OAuthRefresh: refresh})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeModernDiscovery}
	manager.Trigger(serverID, nil, false)
	old := receiveCandidate(t, driver.started)
	driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, OAuthChallenge: disposition}
	<-refresh.started
	refresh.errors <- errors.New("refresh rejected")
	assert.Equal(t, old.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeAuthenticationRequired }, time.Second, time.Millisecond)
	assert.Equal(t, contract.ReasonAuthenticationRejected, *manager.Status(serverID).Reason)
	select {
	case candidate := <-driver.started:
		t.Fatalf("replacement started after refresh failure: %s", candidate.RuntimeID)
	default:
	}
}

func TestManagerOAuthChallengeDrainPreventsReplacement(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	refresh := newChallengeRefreshFake()
	manager, err := New(Options{Repository: repository, Driver: driver, OAuthRefresh: refresh})
	require.NoError(t, err)
	var serverID string
	for serverID = range repository.servers {
		break
	}
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeModernDiscovery}
	manager.Trigger(serverID, nil, false)
	old := receiveCandidate(t, driver.started)
	driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, OAuthChallenge: disposition}
	<-refresh.started
	<-manager.Drain(context.Background())
	repository.mu.Lock()
	authority := repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "2"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "2"}
	assert.Equal(t, old.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	require.Eventually(t, func() bool { return manager.AdmissionStatus().InUse == 0 }, time.Second, time.Millisecond)
	select {
	case candidate := <-driver.started:
		t.Fatalf("replacement started during drain: %s", candidate.RuntimeID)
	default:
	}
}

func TestManagerOAuthChallengeSupersessionStartsIndependentGenerationWithoutReplay(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	catalog := newSequenceCatalog()
	refresh := newChallengeRefreshFake()
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, OAuthRefresh: refresh})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	repository.mu.Lock()
	authority := repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "1"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeModernDiscovery}
	manager.Trigger(serverID, nil, false)
	old := receiveCandidate(t, driver.started)
	driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, OAuthChallenge: disposition}
	<-refresh.started

	manager.Trigger(serverID, nil, false)
	repository.mu.Lock()
	authority = repository.authorities[serverID]
	authority.CredentialRevisions.OAuthTokens = "2"
	repository.authorities[serverID] = authority
	repository.mu.Unlock()
	refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "2"}
	assert.Equal(t, old.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	independent := receiveCandidate(t, driver.started)
	assert.Greater(t, independent.Generation, old.Generation)
	assert.Empty(t, independent.OAuthReplayStage)
	driver.startResult <- activeOutcome()
	assert.Equal(t, independent.RuntimeID, receiveCandidate(t, catalog.started).RuntimeID)
	catalog.outcomes <- CatalogOutcome{State: contract.ActiveCatalogCurrent}
	require.Eventually(t, func() bool { return manager.Status(serverID).State == contract.RuntimeActive }, time.Second, time.Millisecond)
}

func TestManagerOAuthChallengeDoesNotReplaceAfterUnconfirmedStopOrSecondChallenge(t *testing.T) {
	tests := []struct {
		name         string
		stopResult   bool
		second       bool
		expectReason contract.PublicReason
	}{
		{name: "unconfirmed stop", stopResult: false, expectReason: contract.ReasonStopUnconfirmed},
		{name: "second challenge", stopResult: true, second: true, expectReason: contract.ReasonAuthenticationRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := newLifecycleDriver()
			catalog := newSequenceCatalog()
			refresh := newChallengeRefreshFake()
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, OAuthRefresh: refresh})
			require.NoError(t, err)
			defer manager.Shutdown()
			var serverID string
			for serverID = range repository.servers {
				break
			}
			repository.mu.Lock()
			authority := repository.authorities[serverID]
			authority.CredentialRevisions.OAuthTokens = "1"
			repository.authorities[serverID] = authority
			repository.mu.Unlock()
			disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeModernDiscovery}
			manager.Trigger(serverID, nil, false)
			old := receiveCandidate(t, driver.started)
			driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, OAuthChallenge: disposition}
			<-refresh.started
			repository.mu.Lock()
			authority = repository.authorities[serverID]
			authority.CredentialRevisions.OAuthTokens = "2"
			repository.authorities[serverID] = authority
			repository.mu.Unlock()
			refresh.release <- OAuthChallengeRefreshResult{OAuthTokensRevision: "2"}
			assert.Equal(t, old.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
			driver.stopResult <- test.stopResult
			if test.second {
				fresh := receiveCandidate(t, driver.started)
				driver.startResult <- Outcome{State: contract.RuntimeAuthenticationRequired, OAuthChallenge: disposition}
				assert.Equal(t, fresh.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
				driver.stopResult <- true
			}
			require.Eventually(t, func() bool {
				status := manager.Status(serverID)
				return status.State == contract.RuntimeDegraded || status.State == contract.RuntimeAuthenticationRequired
			}, time.Second, time.Millisecond)
			assert.Equal(t, test.expectReason, *manager.Status(serverID).Reason)
			select {
			case <-refresh.started:
				t.Fatal("OAuth challenge refreshed more than once")
			default:
			}
			if !test.stopResult {
				select {
				case candidate := <-driver.started:
					t.Fatalf("replacement started after unconfirmed stop: %s", candidate.RuntimeID)
				default:
				}
			}
		})
	}
}

func TestManagerKeepsHealthyRuntimeOnInitialCatalogFailure(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	reason := contract.ReasonConnectivity
	catalog := outcomeCatalog{outcome: CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &reason, Intent: CatalogTraversalInitial, RuntimeHealth: CatalogRuntimeHealthy}}
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	manager.Trigger(serverID, nil, false)
	candidate := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeActive && status.CatalogState == contract.ActiveCatalogUnavailable && status.RuntimeID != nil && *status.RuntimeID == candidate.RuntimeID
	}, time.Second, time.Millisecond)
	select {
	case stopped := <-driver.stopping:
		t.Fatalf("healthy runtime stopped after initial catalog failure: %s", stopped.RuntimeID)
	default:
	}
}

func TestManagerConsumesCatalogRuntimeLossWithoutTransientActivation(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	failure := ClassifyFailure(downstream.ErrSessionLost)
	catalog := outcomeCatalog{outcome: CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &failure.Reason, Intent: CatalogTraversalInitial, RuntimeHealth: CatalogRuntimeLost, RuntimeFailure: &failure}}
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, Scheduler: newFakeScheduler()})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	manager.Trigger(serverID, nil, false)
	candidate := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	driver.stopResult <- true
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeRetryWait && status.CatalogState == contract.ActiveCatalogUnavailable && status.RuntimeID == nil
	}, time.Second, time.Millisecond)
}

func TestManagerPostCommitCauseTablePreservesOperationAndCleanupPolicy(t *testing.T) {
	tests := []struct {
		name              string
		cause             CatalogPostCommitCause
		expectedOperation contract.ServerOperationState
		expectedReason    contract.PublicReason
		expectCatalogHint bool
		unconfirmed       bool
		fence             bool
		drain             bool
	}{
		{name: "stale", cause: CatalogPostCommitStale, expectedOperation: contract.OperationSuperseded, expectedReason: contract.ReasonSuperseded, expectCatalogHint: true, fence: true},
		{name: "storage", cause: CatalogPostCommitStorage, expectedOperation: contract.OperationRunning, expectedReason: contract.ReasonConnectivity, expectCatalogHint: true},
		{name: "storage_stop_unconfirmed", cause: CatalogPostCommitStorage, expectedOperation: contract.OperationRunning, expectedReason: contract.ReasonStopUnconfirmed, expectCatalogHint: true, unconfirmed: true},
		{name: "drain", cause: CatalogPostCommitDrain, expectedOperation: contract.OperationRunning, expectedReason: contract.ReasonInterrupted, drain: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := newLifecycleDriver()
			catalog := &durableOnlyCatalog{outcome: CatalogOutcome{State: contract.ActiveCatalogAbsent, Phase: CatalogPublicationDurableOnly, Cause: test.cause}, abandoned: make(chan Candidate, 1)}
			invalidations := make(chan contract.Invalidation, 32)
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, Invalidate: func(invalidation contract.Invalidation) { invalidations <- invalidation }})
			require.NoError(t, err)
			defer manager.Shutdown()
			var serverID string
			for serverID = range repository.servers {
				break
			}
			if test.fence {
				catalog.onPublish = func() { manager.Fence(serverID) }
			}
			if test.drain {
				catalog.onPublish = func() { manager.Drain(context.Background()) }
			}
			operation, createErr := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
			require.NoError(t, createErr)
			manager.Trigger(serverID, &operation.Operation.ID, false)
			assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
			candidate := receiveCandidate(t, driver.started)
			for len(invalidations) > 0 {
				<-invalidations
			}
			driver.startResult <- activeOutcome()
			assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, catalog.abandoned).RuntimeID)
			assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
			driver.stopResult <- !test.unconfirmed
			require.Eventually(t, func() bool {
				current, getErr := repository.GetOperation(context.Background(), operation.Operation.ID)
				return getErr == nil && current.State == test.expectedOperation
			}, time.Second, time.Millisecond)
			if test.expectedOperation == contract.OperationSuperseded {
				current, getErr := repository.GetOperation(context.Background(), operation.Operation.ID)
				require.NoError(t, getErr)
				require.NotNil(t, current.Reason)
				assert.Equal(t, test.expectedReason, *current.Reason)
			}
			if test.unconfirmed {
				require.Eventually(t, func() bool {
					status := manager.Status(serverID)
					return status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
				}, time.Second, time.Millisecond)
			}
			seenCatalog, seenOperation := false, false
			relevantKinds := make([]contract.InvalidationKind, 0, len(invalidations))
			for len(invalidations) > 0 {
				invalidation := <-invalidations
				seenCatalog = seenCatalog || invalidation.Kind == contract.InvalidationCatalog
				seenOperation = seenOperation || invalidation.Kind == contract.InvalidationServerOperations
				if invalidation.Kind == contract.InvalidationCatalog || invalidation.Kind == contract.InvalidationServerOperations || invalidation.Kind == contract.InvalidationServers {
					relevantKinds = append(relevantKinds, invalidation.Kind)
				}
			}
			assert.Equal(t, test.expectCatalogHint, seenCatalog)
			assert.Equal(t, test.expectedOperation == contract.OperationSuperseded, seenOperation)
			if test.expectCatalogHint {
				require.NotEmpty(t, relevantKinds)
				assert.Equal(t, contract.InvalidationCatalog, relevantKinds[0])
			}
			if !test.unconfirmed && !test.drain && test.cause == CatalogPostCommitStorage {
				require.Eventually(t, func() bool {
					status := manager.Status(serverID)
					return status.Reason != nil && *status.Reason == test.expectedReason
				}, time.Second, time.Millisecond)
			}
		})
	}
}

func TestManagerFinalizesStatusAndHintsBeforeOperationSuccess(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	catalog := newFinalizationCatalog()
	publisher := newRecordingPublisher()
	var invalidationMu sync.Mutex
	invalidations := make([]contract.Invalidation, 0, 8)
	serverHint := make(chan struct{})
	releaseServerHint := make(chan struct{})
	var serverHintOnce sync.Once
	catalogHintSeen := false
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, Publisher: publisher, Invalidate: func(invalidation contract.Invalidation) {
		invalidationMu.Lock()
		invalidations = append(invalidations, invalidation)
		if invalidation.Kind == contract.InvalidationCatalog {
			catalogHintSeen = true
		}
		blockServerHint := invalidation.Kind == contract.InvalidationServers && catalogHintSeen
		invalidationMu.Unlock()
		if blockServerHint {
			serverHintOnce.Do(func() {
				close(serverHint)
				<-releaseServerHint
			})
		}
	}})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.Trigger(serverID, &operation.Operation.ID, false)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
	candidate := receiveCandidate(t, driver.started)
	invalidationMu.Lock()
	invalidations = nil
	invalidationMu.Unlock()
	driver.startResult <- activeOutcome()
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, catalog.installed).RuntimeID)
	select {
	case <-serverHint:
	case <-time.After(time.Second):
		t.Fatal("manager status hint did not follow catalog installation")
	}
	assert.True(t, catalog.routeVisible())
	status := manager.Status(serverID)
	assert.Equal(t, contract.RuntimeActive, status.State)
	assert.Equal(t, contract.ActiveCatalogCurrent, status.CatalogState)
	assert.Equal(t, candidate.RuntimeID, *status.RuntimeID)
	invalidationMu.Lock()
	kinds := make([]contract.InvalidationKind, 0, len(invalidations))
	for _, invalidation := range invalidations {
		kinds = append(kinds, invalidation.Kind)
	}
	invalidationMu.Unlock()
	assert.Contains(t, kinds, contract.InvalidationCatalog)
	assert.Contains(t, kinds, contract.InvalidationServers)
	assert.NotContains(t, kinds, contract.InvalidationServerOperations)
	current, err := repository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationRunning, current.State)

	close(releaseServerHint)
	assert.Equal(t, contract.OperationSucceeded, (<-repository.transitions).State)
	require.Eventually(t, func() bool {
		invalidationMu.Lock()
		defer invalidationMu.Unlock()
		for _, invalidation := range invalidations {
			if invalidation.Kind == contract.InvalidationServerOperations {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

func TestManagerOperationPersistenceRollbackWithdrawsBeforeUnconfirmedStop(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	catalog := newFinalizationCatalog()
	publisher := newRecordingPublisher()
	invalidations := make(chan contract.Invalidation, 32)
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: catalog, Publisher: publisher, Invalidate: func(invalidation contract.Invalidation) { invalidations <- invalidation }})
	require.NoError(t, err)
	defer manager.Shutdown()
	var serverID string
	for serverID = range repository.servers {
		break
	}
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	repository.beforeTransition = func(state contract.ServerOperationState) error {
		if state == contract.OperationSucceeded {
			return errors.New("storage latch")
		}
		return nil
	}
	manager.Trigger(serverID, &operation.Operation.ID, false)
	assert.Equal(t, "fence", receivePublisherEvent(t, publisher.events).step)
	assert.Equal(t, contract.OperationRunning, (<-repository.transitions).State)
	candidate := receiveCandidate(t, driver.started)
	for len(invalidations) > 0 {
		<-invalidations
	}
	driver.startResult <- activeOutcome()
	assert.Equal(t, candidate.RuntimeID, receiveCandidate(t, catalog.installed).RuntimeID)
	rollbackFence := receivePublisherEvent(t, publisher.events)
	assert.Equal(t, "fence", rollbackFence.step)
	withdrawn := receiveCandidate(t, catalog.withdrawn)
	assert.Equal(t, candidate.RuntimeID, withdrawn.RuntimeID)
	assert.False(t, catalog.routeVisible())
	stopping := receiveCandidate(t, driver.stopping)
	assert.Equal(t, candidate.RuntimeID, stopping.RuntimeID)
	driver.stopResult <- false
	require.Eventually(t, func() bool {
		status := manager.Status(serverID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed && status.CatalogState == contract.ActiveCatalogUnavailable && status.RuntimeID == nil
	}, time.Second, time.Millisecond)
	manager.mu.Lock()
	blocked := cloneCandidate(manager.entries[serverID].blockedStop)
	pending := manager.entries[serverID].pending
	manager.mu.Unlock()
	require.NotNil(t, blocked)
	assert.Equal(t, candidate.RuntimeID, blocked.RuntimeID)
	assert.False(t, pending)
	current, err := repository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationRunning, current.State)
	select {
	case next := <-driver.started:
		t.Fatalf("replacement started after unconfirmed rollback stop: %s", next.RuntimeID)
	default:
	}
	seenCatalog, seenServer, seenOperation := false, false, false
	for len(invalidations) > 0 {
		invalidation := <-invalidations
		seenCatalog = seenCatalog || invalidation.Kind == contract.InvalidationCatalog
		seenServer = seenServer || invalidation.Kind == contract.InvalidationServers
		seenOperation = seenOperation || invalidation.Kind == contract.InvalidationServerOperations
	}
	assert.True(t, seenCatalog)
	assert.True(t, seenServer)
	assert.False(t, seenOperation)
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
	for !seen[contract.InvalidationCatalog] || !seen[contract.InvalidationServers] || !seen[contract.InvalidationServerOperations] || !seen[contract.InvalidationSystemStatus] {
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
