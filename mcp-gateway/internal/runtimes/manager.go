// Package runtimes owns process-local S2 reconciliation and runtime status.
package runtimes

import (
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
	Cleanup(context.Context, Candidate)
}

type AuthorityResolver interface {
	Resolve(context.Context, Candidate) AuthorityOutcome
}

type CatalogCoordinator interface {
	Activate(context.Context, Candidate) CatalogOutcome
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
	Repository Repository
	Driver     Driver
	Authority  AuthorityResolver
	Catalog    CatalogCoordinator
	Scheduler  Scheduler
}

type Manager struct {
	mu          sync.Mutex
	repository  Repository
	driver      Driver
	authority   AuthorityResolver
	catalog     CatalogCoordinator
	scheduler   Scheduler
	ctx         context.Context
	cancel      context.CancelFunc
	entries     map[string]*entry
	globalInUse int64
	globalLimit int64
	drainEpoch  uint64
	draining    bool
}

type entry struct {
	generation   uint64
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

type unavailableDriver struct{}

func (unavailableDriver) Reconcile(context.Context, Candidate) Outcome {
	reason := contract.ReasonProtocolUnsupported
	return Outcome{State: contract.RuntimeDegraded, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent, Reason: &reason}
}
func (unavailableDriver) Cleanup(context.Context, Candidate) {}

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
	limit, ok := contract.FixedLimitByName("server_reconciliations")
	if !ok {
		return nil, errors.New("server reconciliation limit is missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{repository: options.Repository, driver: options.Driver, authority: options.Authority, catalog: options.Catalog, scheduler: options.Scheduler, ctx: ctx, cancel: cancel, entries: make(map[string]*entry), globalLimit: limit.Maximum}, nil
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.repository.InterruptNonterminal(ctx); err != nil {
		return err
	}
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
	current := manager.entryLocked(serverID)
	current.status.State = contract.RuntimeDegraded
	current.status.Reason = &reason
}

func (manager *Manager) initializeInactive(server servers.Server) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entryLocked(server.ID)
	state := contract.RuntimeInactive
	if server.DesiredState == contract.DesiredServerDeleted {
		state = contract.RuntimeDeleted
	}
	current.status.State = state
}

func (manager *Manager) Trigger(serverID string, operationID *string, resetBackoff bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	current := manager.entryLocked(serverID)
	current.generation++
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
		current.status.Reconciliation = contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}
		go manager.reconcile(serverID, generation, operationID)
	}
}

func (manager *Manager) reconcile(serverID string, generation uint64, operationID *string) {
	if operationID != nil {
		if _, err := manager.transitionCurrent(serverID, generation, *operationID, contract.OperationRunning, nil); err != nil {
			operation, getErr := manager.repository.GetOperation(manager.ctx, *operationID)
			if getErr != nil || operation.State != contract.OperationRunning {
				manager.finishStale(serverID, generation)
				return
			}
		}
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
	if server.DesiredState != contract.DesiredServerEnabled {
		state := contract.RuntimeInactive
		if server.DesiredState == contract.DesiredServerDeleted {
			state = contract.RuntimeDeleted
		}
		manager.finishSuccess(serverID, generation, operationID, Outcome{State: state, CredentialState: contract.ServerCredentialNotRequired, CatalogState: contract.ActiveCatalogAbsent}, "")
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
	if authorityOutcome.State == "" {
		outcome = manager.driver.Reconcile(manager.ctx, candidate)
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
		manager.driver.Cleanup(context.Background(), candidate)
		manager.finishStale(serverID, generation)
		return
	}
	if outcome.Retryable {
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
		reason := contract.ReasonConnectivity
		if outcome.Reason != nil {
			reason = *outcome.Reason
		}
		manager.finishFailure(serverID, generation, operationID, outcome.State, outcome.CredentialState, outcome.CatalogState, reason, false)
		return
	}
	manager.finishSuccess(serverID, generation, operationID, outcome, runtimeID)
}

func sameFence(server servers.Server, authority servers.AuthorityMetadata, current servers.Server, currentAuthority servers.AuthorityMetadata) bool {
	return server.DesiredRevision == current.DesiredRevision && server.DesiredState == current.DesiredState && authority.RegistrationRevision == currentAuthority.RegistrationRevision && authority.CredentialRevisions == currentAuthority.CredentialRevisions
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

func (manager *Manager) finishSuccess(serverID string, generation uint64, operationID *string, outcome Outcome, runtimeID string) {
	if !manager.generationCurrent(serverID, generation) {
		manager.finishStale(serverID, generation)
		return
	}
	if operationID != nil {
		if _, err := manager.transitionCurrent(serverID, generation, *operationID, contract.OperationSucceeded, nil); err != nil {
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
	current.status.State = outcome.State
	current.status.Reason = cloneReason(outcome.Reason)
	current.status.CredentialState = outcome.CredentialState
	current.status.CatalogState = outcome.CatalogState
	current.status.RuntimeID = nil
	if runtimeID != "" && outcome.State == contract.RuntimeActive {
		current.status.RuntimeID = &runtimeID
	}
	current.retryAttempt = 0
	manager.releaseLocked(current)
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
}

func (manager *Manager) transitionCurrent(serverID string, generation uint64, operationID string, state contract.ServerOperationState, reason *contract.PublicReason) (servers.Operation, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil || current.generation != generation || manager.draining {
		return servers.Operation{}, servers.ErrStaleRevision
	}
	return manager.repository.TransitionOperation(context.Background(), operationID, state, reason)
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

func (manager *Manager) AdmissionStatus() contract.LimitStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return contract.LimitStatus{InUse: manager.globalInUse, Limit: manager.globalLimit, Saturated: manager.globalInUse >= manager.globalLimit}
}

func (manager *Manager) Shutdown() {
	manager.mu.Lock()
	if manager.draining {
		manager.mu.Unlock()
		return
	}
	manager.draining = true
	manager.drainEpoch++
	for _, current := range manager.entries {
		current.generation++
		current.pending = false
		if current.timer != nil {
			current.timer.Stop()
			current.timer = nil
		}
	}
	manager.cancel()
	manager.mu.Unlock()
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
