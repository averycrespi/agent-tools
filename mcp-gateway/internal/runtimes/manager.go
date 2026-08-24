// Package runtimes owns process-local S2 reconciliation and runtime status.
package runtimes

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type Repository interface {
	Get(context.Context, string) (servers.Server, error)
	ListServers(context.Context, *servers.SnapshotCursor, int) (servers.ServerPage, error)
	Authority(context.Context, string) (servers.AuthorityMetadata, error)
	CreateOperation(context.Context, servers.OperationRequest) (servers.OperationResult, error)
	GetOperation(context.Context, string) (servers.Operation, error)
	TransitionOperation(context.Context, string, contract.ServerOperationState, *contract.PublicReason) (servers.Operation, error)
	InterruptNonterminal(context.Context) error
	NewID() (string, error)
}

type Timer interface {
	Stop() bool
}

type Scheduler interface {
	AfterFunc(time.Duration, func()) Timer
}

type Driver interface {
	Reconcile(context.Context, Candidate) Outcome
	Stop(context.Context, Candidate) bool
}

type ActivePublisher interface {
	Fence(string, uint64)
	Withdraw(Candidate)
	Publish(Candidate) bool
}

type AuthorityResolver interface {
	Resolve(context.Context, Candidate) AuthorityOutcome
}

type CatalogCoordinator interface {
	Activate(context.Context, Candidate) CatalogOutcome
}

type CredentialLifecycle interface {
	ReconcileCredentials(context.Context, servers.Operation, servers.Server, servers.AuthorityMetadata, contract.ServerCredentialState) (CredentialLifecycleOutcome, bool)
}

type CredentialLifecycleOutcome struct {
	CredentialState contract.ServerCredentialState
	Reason          *contract.PublicReason
	CleanupPending  bool
}

type AuthorityOutcome struct {
	State           contract.RuntimeState
	CredentialState contract.ServerCredentialState
	Reason          *contract.PublicReason
	Retryable       bool
}

type CatalogOutcome struct {
	State  contract.ActiveCatalogState
	Reason *contract.PublicReason
}

type Candidate struct {
	Server      servers.Server
	Authority   servers.AuthorityMetadata
	RuntimeID   string
	OperationID *string
	Generation  uint64
	DrainEpoch  uint64
}

type Outcome struct {
	State           contract.RuntimeState
	CredentialState contract.ServerCredentialState
	CatalogState    contract.ActiveCatalogState
	Reason          *contract.PublicReason
	Retryable       bool
}

type Status struct {
	State           contract.RuntimeState
	Reason          *contract.PublicReason
	RuntimeID       *string
	CredentialState contract.ServerCredentialState
	CatalogState    contract.ActiveCatalogState
	Reconciliation  contract.LimitStatus
	RetryAttempt    int
}

type Options struct {
	Repository  Repository
	Driver      Driver
	Authority   AuthorityResolver
	Catalog     CatalogCoordinator
	Credentials CredentialLifecycle
	Scheduler   Scheduler
	Invalidate  func(contract.Invalidation)
	Publisher   ActivePublisher
}

type DrainResult struct {
	Verified    int
	Unconfirmed int
}

type Manager struct {
	mu          sync.Mutex
	repository  Repository
	driver      Driver
	authority   AuthorityResolver
	catalog     CatalogCoordinator
	credentials CredentialLifecycle
	scheduler   Scheduler
	invalidate  func(contract.Invalidation)
	publisher   ActivePublisher
	ctx         context.Context
	cancel      context.CancelFunc
	entries     map[string]*entry
	globalInUse int64
	globalLimit int64
	drainEpoch  uint64
	draining    bool
	drainDone   chan DrainResult
}

type entry struct {
	generation   uint64
	active       *Candidate
	blockedStop  *Candidate
	running      bool
	pending      bool
	operationID  *string
	timer        Timer
	timerVersion uint64
	retryAttempt int
	status       Status
}

type systemScheduler struct{}

type systemTimer struct{ timer *time.Timer }

func (systemScheduler) AfterFunc(delay time.Duration, callback func()) Timer {
	return systemTimer{timer: time.AfterFunc(delay, callback)}
}
func (timer systemTimer) Stop() bool { return timer.timer.Stop() }

type readyAuthority struct{}

func (readyAuthority) Resolve(context.Context, Candidate) AuthorityOutcome {
	return AuthorityOutcome{CredentialState: contract.ServerCredentialNotRequired}
}

type absentCatalog struct{}

func (absentCatalog) Activate(context.Context, Candidate) CatalogOutcome {
	return CatalogOutcome{State: contract.ActiveCatalogAbsent}
}

type memoryPublisher struct {
	mu     sync.Mutex
	fences map[string]uint64
	active map[string]Candidate
}

func newMemoryPublisher() *memoryPublisher {
	return &memoryPublisher{fences: make(map[string]uint64), active: make(map[string]Candidate)}
}

func (publisher *memoryPublisher) Fence(serverID string, generation uint64) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if generation > publisher.fences[serverID] {
		publisher.fences[serverID] = generation
	}
	delete(publisher.active, serverID)
}

func (publisher *memoryPublisher) Withdraw(candidate Candidate) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	current, ok := publisher.active[candidate.Server.ID]
	if ok && current.RuntimeID == candidate.RuntimeID {
		delete(publisher.active, candidate.Server.ID)
	}
}

func (publisher *memoryPublisher) Publish(candidate Candidate) bool {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.fences[candidate.Server.ID] != candidate.Generation {
		return false
	}
	publisher.active[candidate.Server.ID] = candidate
	return true
}

type unavailableDriver struct{}

func (unavailableDriver) Reconcile(context.Context, Candidate) Outcome {
	reason := contract.ReasonProtocolUnsupported
	return Outcome{State: contract.RuntimeDegraded, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason}
}
func (unavailableDriver) Stop(context.Context, Candidate) bool { return true }

func New(options Options) (*Manager, error) {
	if options.Repository == nil {
		return nil, errors.New("runtime repository is required")
	}
	if options.Driver == nil {
		options.Driver = unavailableDriver{}
	}
	if options.Authority == nil {
		options.Authority = readyAuthority{}
	}
	if options.Catalog == nil {
		options.Catalog = absentCatalog{}
	}
	if options.Scheduler == nil {
		options.Scheduler = systemScheduler{}
	}
	if options.Publisher == nil {
		options.Publisher = newMemoryPublisher()
	}
	limit, ok := contract.FixedLimitByName("server_reconciliations")
	if !ok {
		return nil, errors.New("server reconciliation limit is missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{repository: options.Repository, driver: options.Driver, authority: options.Authority, catalog: options.Catalog, credentials: options.Credentials, scheduler: options.Scheduler, invalidate: options.Invalidate, publisher: options.Publisher, ctx: ctx, cancel: cancel, entries: make(map[string]*entry), globalLimit: limit.Maximum}, nil
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.repository.InterruptNonterminal(ctx); err != nil {
		return err
	}
	manager.publish(contract.InvalidationServerOperations, nil)
	manager.publish(contract.InvalidationSystemStatus, nil)
	var cursor *servers.SnapshotCursor
	for {
		page, err := manager.repository.ListServers(ctx, cursor, contract.S2ListPageDefault)
		if err != nil {
			return err
		}
		for _, server := range page.Items {
			if server.DesiredState != contract.DesiredServerEnabled {
				manager.initializeInactive(server)
				continue
			}
			operation, err := manager.repository.CreateOperation(ctx, servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationActivate, ExpectedDesiredRevision: server.DesiredRevision})
			if err != nil {
				manager.initializeFailure(server.ID, contract.ReasonConnectivity)
				continue
			}
			manager.publish(contract.InvalidationServerOperations, &operation.Operation.ID)
			manager.Trigger(server.ID, &operation.Operation.ID, true)
		}
		if page.Next == nil {
			return nil
		}
		cursor = page.Next
	}
}

func (manager *Manager) initializeFailure(serverID string, reason contract.PublicReason) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	current := manager.entryLocked(serverID)
	current.status.State = contract.RuntimeDegraded
	current.status.Reason = &reason
	manager.publish(contract.InvalidationServers, &serverID)
}

func (manager *Manager) initializeInactive(server servers.Server) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	current := manager.entryLocked(server.ID)
	state := contract.RuntimeInactive
	if server.DesiredState == contract.DesiredServerDeleted {
		state = contract.RuntimeDeleted
	}
	current.status.State = state
	manager.publish(contract.InvalidationServers, &server.ID)
}

func (manager *Manager) SetAuthorityResolver(resolver AuthorityResolver) {
	if resolver == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.authority = resolver
}

func (manager *Manager) SetCredentialLifecycle(lifecycle CredentialLifecycle) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.credentials = lifecycle
}

func (manager *Manager) SetCredentialState(serverID string, state contract.ServerCredentialState, withdraw bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	current := manager.entryLocked(serverID)
	current.status.CredentialState = state
	if withdraw {
		current.generation++
		manager.publisher.Fence(serverID, current.generation)
	}
	manager.publish(contract.InvalidationServers, &serverID)
}

func (manager *Manager) Fence(serverID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entryLocked(serverID)
	current.generation++
	manager.publisher.Fence(serverID, current.generation)
}

func (manager *Manager) Trigger(serverID string, operationID *string, resetBackoff bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	current := manager.entryLocked(serverID)
	current.generation++
	manager.publisher.Fence(serverID, current.generation)
	current.pending = true
	current.operationID = cloneString(operationID)
	if current.timer != nil {
		current.timer.Stop()
		current.timer = nil
		current.timerVersion++
	}
	if resetBackoff {
		current.retryAttempt = 0
	}
	manager.startAvailableLocked()
}

func (manager *Manager) entryLocked(serverID string) *entry {
	current := manager.entries[serverID]
	if current == nil {
		current = &entry{status: Status{State: contract.RuntimeInactive, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}}
		manager.entries[serverID] = current
	}
	return current
}

func (manager *Manager) startAvailableLocked() {
	if manager.draining {
		return
	}
	for serverID, current := range manager.entries {
		if manager.globalInUse >= manager.globalLimit {
			return
		}
		if current.running || !current.pending {
			continue
		}
		current.running = true
		current.pending = false
		manager.globalInUse++
		generation := current.generation
		operationID := cloneString(current.operationID)
		current.operationID = nil
		current.status.State = contract.RuntimeActivating
		if current.active != nil || current.blockedStop != nil {
			current.status.State = contract.RuntimeStopping
		}
		current.status.Reconciliation = contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}
		manager.publish(contract.InvalidationServers, &serverID)
		manager.publish(contract.InvalidationSystemStatus, nil)
		go manager.reconcile(serverID, generation, operationID)
	}
}

func (manager *Manager) reconcile(serverID string, generation uint64, operationID *string) {
	var operation *servers.Operation
	if operationID != nil {
		transitioned, err := manager.transitionCurrent(serverID, generation, *operationID, contract.OperationRunning, nil)
		if err != nil {
			transitioned, err = manager.repository.GetOperation(manager.ctx, *operationID)
			if err != nil || transitioned.State != contract.OperationRunning {
				manager.finishStale(serverID, generation)
				return
			}
		}
		operation = &transitioned
		if transitioned.Kind == contract.OperationDisconnectCredentials || transitioned.Kind == contract.OperationDelete {
			manager.mu.Lock()
			current := manager.entries[serverID]
			if current != nil && current.generation == generation && !manager.draining {
				current.status.CredentialState = contract.ServerCredentialDisconnecting
				manager.publish(contract.InvalidationServers, &serverID)
			}
			manager.mu.Unlock()
		}
	}
	if !manager.stopPrevious(serverID) {
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonStopUnconfirmed, false)
		return
	}
	server, err := manager.repository.Get(manager.ctx, serverID)
	if err != nil {
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonConnectivity, false)
		return
	}
	authority, err := manager.repository.Authority(manager.ctx, serverID)
	if err != nil {
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonKeyringUnavailable, true)
		return
	}
	if operation != nil && manager.credentials != nil {
		manager.mu.Lock()
		credentialState := manager.entryLocked(serverID).status.CredentialState
		manager.mu.Unlock()
		credentialOutcome, handled := manager.credentials.ReconcileCredentials(manager.ctx, *operation, server, authority, credentialState)
		if handled {
			state := contract.RuntimeInactive
			if server.DesiredState == contract.DesiredServerDeleted {
				state = contract.RuntimeDeleted
			}
			if credentialOutcome.CleanupPending {
				manager.finishFailure(serverID, generation, operationID, state, contract.ServerCredentialCleanupPending, contract.ActiveCatalogAbsent, contract.ReasonCleanupPending, false)
				return
			}
			manager.finishSuccess(serverID, generation, operationID, Outcome{State: state, CredentialState: credentialOutcome.CredentialState, CatalogState: contract.ActiveCatalogAbsent, Reason: credentialOutcome.Reason}, nil)
			return
		}
	}
	if server.DesiredState != contract.DesiredServerEnabled {
		state := contract.RuntimeInactive
		if server.DesiredState == contract.DesiredServerDeleted {
			state = contract.RuntimeDeleted
		}
		manager.finishSuccess(serverID, generation, operationID, Outcome{State: state, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}, nil)
		return
	}
	runtimeID, err := manager.repository.NewID()
	if err != nil {
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonResourceLimit, true)
		return
	}
	candidate := Candidate{Server: server, Authority: authority, RuntimeID: runtimeID, OperationID: cloneString(operationID), Generation: generation, DrainEpoch: manager.currentDrainEpoch()}
	authorityOutcome := manager.authority.Resolve(manager.ctx, candidate)
	outcome := Outcome{State: authorityOutcome.State, CredentialState: authorityOutcome.CredentialState, CatalogState: contract.ActiveCatalogAbsent, Reason: authorityOutcome.Reason, Retryable: authorityOutcome.Retryable}
	started := false
	if authorityOutcome.State == "" {
		outcome = manager.driver.Reconcile(manager.ctx, candidate)
		started = true
		if outcome.CredentialState == "" {
			outcome.CredentialState = authorityOutcome.CredentialState
		}
		if outcome.State == contract.RuntimeActive {
			catalog := manager.catalog.Activate(manager.ctx, candidate)
			outcome.CatalogState = catalog.State
			if outcome.CatalogState == "" {
				outcome.CatalogState = contract.ActiveCatalogAbsent
			}
			if catalog.Reason != nil {
				outcome.Reason = catalog.Reason
			}
		}
	}
	currentServer, serverErr := manager.repository.Get(manager.ctx, serverID)
	currentAuthority, authorityErr := manager.repository.Authority(manager.ctx, serverID)
	if serverErr != nil || authorityErr != nil || !sameFence(server, authority, currentServer, currentAuthority) || !manager.current(serverID, generation, candidate.DrainEpoch) {
		if started && !manager.stopCandidate(candidate) {
			manager.rememberBlockedStop(serverID, candidate)
		}
		manager.finishStale(serverID, generation)
		return
	}
	if outcome.Retryable {
		if started && !manager.stopCandidate(candidate) {
			manager.rememberBlockedStop(serverID, candidate)
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, outcome.CredentialState, outcome.CatalogState, contract.ReasonStopUnconfirmed, false)
			return
		}
		reason := contract.ReasonConnectivity
		if outcome.Reason != nil {
			reason = *outcome.Reason
		}
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeRetryWait, outcome.CredentialState, outcome.CatalogState, reason, true)
		return
	}
	if outcome.State == "" {
		outcome.State = contract.RuntimeActive
	}
	if outcome.State != contract.RuntimeActive && outcome.State != contract.RuntimeInactive && outcome.State != contract.RuntimeDeleted {
		if started && !manager.stopCandidate(candidate) {
			manager.rememberBlockedStop(serverID, candidate)
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, outcome.CredentialState, outcome.CatalogState, contract.ReasonStopUnconfirmed, false)
			return
		}
		reason := contract.ReasonConnectivity
		if outcome.Reason != nil {
			reason = *outcome.Reason
		}
		manager.finishFailure(serverID, generation, operationID, outcome.State, outcome.CredentialState, outcome.CatalogState, reason, false)
		return
	}
	if outcome.State != contract.RuntimeActive {
		if started && !manager.stopCandidate(candidate) {
			manager.rememberBlockedStop(serverID, candidate)
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, outcome.CredentialState, outcome.CatalogState, contract.ReasonStopUnconfirmed, false)
			return
		}
		manager.finishSuccess(serverID, generation, operationID, outcome, nil)
		return
	}
	manager.finishSuccess(serverID, generation, operationID, outcome, &candidate)
}

func (manager *Manager) stopPrevious(serverID string) bool {
	manager.mu.Lock()
	current := manager.entries[serverID]
	var previous *Candidate
	if current != nil {
		if current.blockedStop != nil {
			previous = cloneCandidate(current.blockedStop)
		} else if current.active != nil {
			previous = cloneCandidate(current.active)
			current.active = nil
		}
	}
	manager.mu.Unlock()
	if previous == nil {
		return true
	}
	if !manager.stopCandidate(*previous) {
		manager.rememberBlockedStop(serverID, *previous)
		return false
	}
	manager.mu.Lock()
	current = manager.entries[serverID]
	if current != nil && current.blockedStop != nil && current.blockedStop.RuntimeID == previous.RuntimeID {
		current.blockedStop = nil
	}
	manager.mu.Unlock()
	return true
}

func (manager *Manager) stopCandidate(candidate Candidate) bool {
	manager.publisher.Withdraw(candidate)
	return manager.driver.Stop(context.Background(), candidate)
}

func (manager *Manager) rememberBlockedStop(serverID string, candidate Candidate) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entryLocked(serverID)
	current.blockedStop = cloneCandidate(&candidate)
}

func sameFence(server servers.Server, authority servers.AuthorityMetadata, current servers.Server, currentAuthority servers.AuthorityMetadata) bool {
	return server.DesiredState == current.DesiredState && bytes.Equal(server.Transport, current.Transport) && authority.RegistrationRevision == currentAuthority.RegistrationRevision && authority.CredentialRevisions == currentAuthority.CredentialRevisions
}

func (manager *Manager) current(serverID string, generation, drainEpoch uint64) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	return current != nil && current.generation == generation && manager.drainEpoch == drainEpoch && !manager.draining
}

func (manager *Manager) currentDrainEpoch() uint64 {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.drainEpoch
}

func (manager *Manager) finishSuccess(serverID string, generation uint64, operationID *string, outcome Outcome, candidate *Candidate) {
	manager.mu.Lock()
	current := manager.entries[serverID]
	if current == nil || current.generation != generation || manager.draining {
		manager.mu.Unlock()
		if candidate != nil {
			if !manager.stopCandidate(*candidate) {
				manager.rememberBlockedStop(serverID, *candidate)
			}
		}
		manager.finishStale(serverID, generation)
		return
	}
	published := false
	if candidate != nil && outcome.State == contract.RuntimeActive {
		if !manager.publisher.Publish(*candidate) {
			manager.mu.Unlock()
			if !manager.stopCandidate(*candidate) {
				manager.rememberBlockedStop(serverID, *candidate)
			}
			manager.finishStale(serverID, generation)
			return
		}
		published = true
		current.active = cloneCandidate(candidate)
	}
	if operationID != nil {
		if _, err := manager.repository.TransitionOperation(context.Background(), *operationID, contract.OperationSucceeded, outcome.Reason); err != nil {
			if published {
				manager.publisher.Withdraw(*candidate)
				current.active = nil
			}
			manager.mu.Unlock()
			if candidate != nil && !manager.stopCandidate(*candidate) {
				manager.rememberBlockedStop(serverID, *candidate)
			}
			manager.finishStale(serverID, generation)
			return
		}
		manager.publish(contract.InvalidationServerOperations, operationID)
	}
	current.status.State = outcome.State
	current.status.Reason = cloneReason(outcome.Reason)
	current.status.CredentialState = outcome.CredentialState
	current.status.CatalogState = outcome.CatalogState
	current.status.RuntimeID = nil
	if candidate != nil && outcome.State == contract.RuntimeActive {
		current.status.RuntimeID = cloneString(&candidate.RuntimeID)
	}
	current.retryAttempt = 0
	manager.releaseLocked(current)
	manager.publish(contract.InvalidationServers, &serverID)
	manager.publish(contract.InvalidationSystemStatus, nil)
	manager.mu.Unlock()
}

func (manager *Manager) finishFailure(serverID string, generation uint64, operationID *string, state contract.RuntimeState, credential contract.ServerCredentialState, catalog contract.ActiveCatalogState, reason contract.PublicReason, retry bool) {
	if !manager.generationCurrent(serverID, generation) {
		manager.finishStale(serverID, generation)
		return
	}
	if operationID != nil {
		if _, err := manager.transitionCurrent(serverID, generation, *operationID, contract.OperationFailed, &reason); err != nil {
			manager.finishStale(serverID, generation)
			return
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil || current.generation != generation {
		manager.releaseLocked(current)
		return
	}
	current.status.State = state
	current.status.Reason = &reason
	current.status.RuntimeID = nil
	current.status.CredentialState = credential
	current.status.CatalogState = catalog
	manager.releaseLocked(current)
	if retry && !manager.draining {
		manager.scheduleRetryLocked(serverID, current)
	}
	manager.publish(contract.InvalidationServers, &serverID)
	manager.publish(contract.InvalidationSystemStatus, nil)
}

func (manager *Manager) transitionCurrent(serverID string, generation uint64, operationID string, state contract.ServerOperationState, reason *contract.PublicReason) (servers.Operation, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil || current.generation != generation || manager.draining {
		return servers.Operation{}, servers.ErrStaleRevision
	}
	operation, err := manager.repository.TransitionOperation(context.Background(), operationID, state, reason)
	if err == nil {
		manager.publish(contract.InvalidationServerOperations, &operationID)
		manager.publish(contract.InvalidationSystemStatus, nil)
	}
	return operation, err
}

func (manager *Manager) generationCurrent(serverID string, generation uint64) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	return current != nil && current.generation == generation && !manager.draining
}

func (manager *Manager) finishStale(serverID string, generation uint64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil {
		manager.globalInUse--
		manager.startAvailableLocked()
		return
	}
	if current.generation == generation {
		current.pending = true
	}
	manager.releaseLocked(current)
}

func (manager *Manager) releaseLocked(current *entry) {
	if current != nil {
		current.running = false
		current.status.Reconciliation = contract.LimitStatus{Limit: 1}
	}
	if manager.globalInUse > 0 {
		manager.globalInUse--
	}
	manager.startAvailableLocked()
}

func (manager *Manager) scheduleRetryLocked(serverID string, current *entry) {
	delays := contract.ReconciliationRetryDelays()
	index := current.retryAttempt
	if index >= len(delays) {
		index = len(delays) - 1
	}
	delay := delays[index]
	if current.retryAttempt < len(delays)-1 {
		current.retryAttempt++
	}
	current.status.RetryAttempt = current.retryAttempt
	current.timerVersion++
	version := current.timerVersion
	generation := current.generation
	current.timer = manager.scheduler.AfterFunc(delay, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		entry := manager.entries[serverID]
		if entry == nil || manager.draining || entry.timerVersion != version || entry.generation != generation {
			return
		}
		entry.timer = nil
		entry.generation++
		manager.publisher.Fence(serverID, entry.generation)
		entry.pending = true
		manager.startAvailableLocked()
	})
}

func (manager *Manager) Status(serverID string) Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entryLocked(serverID)
	result := current.status
	result.Reason = cloneReason(result.Reason)
	result.RuntimeID = cloneString(result.RuntimeID)
	result.RetryAttempt = current.retryAttempt
	return result
}

func (manager *Manager) OperationState(_ context.Context, serverID string) servers.OperationTriggerState {
	status := manager.Status(serverID)
	return servers.OperationTriggerState{RuntimeState: status.State, RuntimeReason: status.Reason, CredentialState: status.CredentialState, CatalogState: status.CatalogState}
}

func (manager *Manager) RuntimeFailed(serverID, runtimeID string, reason contract.PublicReason) bool {
	manager.mu.Lock()
	current := manager.entries[serverID]
	if current == nil || current.active == nil || current.active.RuntimeID != runtimeID || manager.draining {
		manager.mu.Unlock()
		return false
	}
	failed := cloneCandidate(current.active)
	current.active = nil
	current.generation++
	generation := current.generation
	manager.publisher.Fence(serverID, generation)
	current.status.State = contract.RuntimeStopping
	current.status.Reason = &reason
	current.status.RuntimeID = nil
	manager.publish(contract.InvalidationServers, &serverID)
	manager.publish(contract.InvalidationSystemStatus, nil)
	manager.mu.Unlock()
	go manager.finishRuntimeFailure(serverID, generation, *failed, reason)
	return true
}

func (manager *Manager) finishRuntimeFailure(serverID string, generation uint64, failed Candidate, reason contract.PublicReason) {
	if !manager.stopCandidate(failed) {
		manager.rememberBlockedStop(serverID, failed)
		manager.mu.Lock()
		current := manager.entries[serverID]
		if current != nil && current.generation == generation {
			current.status.State = contract.RuntimeDegraded
			stopReason := contract.ReasonStopUnconfirmed
			current.status.Reason = &stopReason
			manager.publish(contract.InvalidationServers, &serverID)
		}
		manager.mu.Unlock()
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil || current.generation != generation || manager.draining {
		return
	}
	current.status.State = contract.RuntimeRetryWait
	current.status.Reason = &reason
	manager.scheduleRetryLocked(serverID, current)
	manager.publish(contract.InvalidationServers, &serverID)
	manager.publish(contract.InvalidationSystemStatus, nil)
}

func (manager *Manager) AdmissionStatus() contract.LimitStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return contract.LimitStatus{InUse: manager.globalInUse, Limit: manager.globalLimit, Saturated: manager.globalInUse >= manager.globalLimit}
}

func (manager *Manager) RuntimeStatus() contract.LimitStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var inUse int64
	for _, current := range manager.entries {
		if current.status.RuntimeID != nil || current.active != nil || current.blockedStop != nil {
			inUse++
		}
	}
	limit, _ := contract.FixedLimitByName("downstream_runtimes")
	return contract.LimitStatus{InUse: inUse, Limit: limit.Maximum, Saturated: inUse >= limit.Maximum}
}

func (manager *Manager) Drain(ctx context.Context) <-chan DrainResult {
	manager.mu.Lock()
	if manager.drainDone != nil {
		done := manager.drainDone
		manager.mu.Unlock()
		return done
	}
	manager.draining = true
	manager.drainEpoch++
	manager.drainDone = make(chan DrainResult, 1)
	done := manager.drainDone
	candidates := make([]Candidate, 0, len(manager.entries))
	seen := make(map[string]struct{}, len(manager.entries))
	for serverID, current := range manager.entries {
		current.generation++
		manager.publisher.Fence(serverID, current.generation)
		current.pending = false
		if current.timer != nil {
			current.timer.Stop()
			current.timer = nil
		}
		owned := current.active != nil || current.blockedStop != nil
		for _, candidate := range []*Candidate{current.active, current.blockedStop} {
			if candidate == nil {
				continue
			}
			manager.publisher.Withdraw(*candidate)
			if _, duplicate := seen[candidate.RuntimeID]; !duplicate {
				seen[candidate.RuntimeID] = struct{}{}
				candidates = append(candidates, *candidate)
			}
		}
		current.active = nil
		current.blockedStop = nil
		current.status.RuntimeID = nil
		if owned {
			reason := contract.ReasonStopUnconfirmed
			current.status.State = contract.RuntimeDegraded
			current.status.Reason = &reason
		}
	}
	manager.cancel()
	manager.mu.Unlock()

	go func() {
		results := make(chan bool, len(candidates))
		for _, candidate := range candidates {
			candidate := candidate
			go func() { results <- manager.driver.Stop(ctx, candidate) }()
		}
		result := DrainResult{}
		for range candidates {
			if <-results {
				result.Verified++
			} else {
				result.Unconfirmed++
			}
		}
		done <- result
		close(done)
	}()
	return done
}

func (manager *Manager) Shutdown() {
	manager.Drain(context.Background())
}

func (manager *Manager) publish(kind contract.InvalidationKind, resourceID *string) {
	if manager.invalidate != nil {
		manager.invalidate(contract.Invalidation{Kind: kind, ResourceID: cloneString(resourceID)})
	}
}

func cloneCandidate(value *Candidate) *Candidate {
	if value == nil {
		return nil
	}
	copy := *value
	copy.OperationID = cloneString(value.OperationID)
	copy.Server.Transport = append([]byte(nil), value.Server.Transport...)
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneReason(value *contract.PublicReason) *contract.PublicReason {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
