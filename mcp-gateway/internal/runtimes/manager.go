// Package runtimes owns process-local S2 reconciliation and runtime status.
package runtimes

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
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
	Reconcile(context.Context, Candidate, *MaterialLease) Outcome
	Stop(context.Context, Candidate) bool
}

type ActivePublisher interface {
	Fence(string, uint64)
	Withdraw(Candidate)
}

type AuthorityResolver interface {
	Resolve(context.Context, Candidate) AuthorityOutcome
}

type OAuthChallengeRefreshRequest struct {
	ServerID                     string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedOAuthClientRevision  string
	ExpectedOAuthTokensRevision  string
	ChallengeMetadata            []string
}

type OAuthChallengeRefreshResult struct {
	OAuthTokensRevision string
}

type OAuthChallengeRefresher interface {
	RefreshOAuthChallenge(context.Context, OAuthChallengeRefreshRequest) (OAuthChallengeRefreshResult, error)
}

type OAuthStepUpper interface {
	StageStepUp(string, []string, []string, []string) error
}

type CatalogCoordinator interface {
	Activate(context.Context, Candidate) CatalogOutcome
}

type CatalogRefresher interface {
	CatalogCoordinator
	Refresh(context.Context, Candidate) CatalogOutcome
	Withdraw(Candidate, contract.ActiveCatalogState)
}

type CatalogAbandoner interface {
	Abandon(Candidate)
}

type CatalogLifecycleFinalizer interface {
	FinalizeLifecycle(context.Context, servers.Server, servers.AuthorityMetadata, contract.DurableCatalogState, contract.ActiveCatalogState) error
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
	Lease           *MaterialLease
}

type CatalogPublicationPhase string

const (
	CatalogPublicationNone        CatalogPublicationPhase = ""
	CatalogPublicationDurableOnly CatalogPublicationPhase = "durable_only"
	CatalogPublicationInstalled   CatalogPublicationPhase = "installed"
)

type CatalogPostCommitCause string

const (
	CatalogPostCommitStale   CatalogPostCommitCause = "stale"
	CatalogPostCommitDrain   CatalogPostCommitCause = "drain"
	CatalogPostCommitStorage CatalogPostCommitCause = "storage"
)

type CatalogTraversalIntent string

const (
	CatalogTraversalInitial CatalogTraversalIntent = "initial"
	CatalogTraversalRefresh CatalogTraversalIntent = "refresh"
	CatalogTraversalPoll    CatalogTraversalIntent = "poll"
)

type CatalogRuntimeHealth string

const (
	CatalogRuntimeHealthy CatalogRuntimeHealth = "healthy"
	CatalogRuntimeLost    CatalogRuntimeHealth = "lost"
)

type CatalogOutcome struct {
	State          contract.ActiveCatalogState
	Reason         *contract.PublicReason
	Phase          CatalogPublicationPhase
	Cause          CatalogPostCommitCause
	Intent         CatalogTraversalIntent
	RuntimeHealth  CatalogRuntimeHealth
	RuntimeFailure *FailureDisposition
	OAuthChallenge *downstream.OAuthChallengeDisposition
}

type Candidate struct {
	Server           servers.Server
	Authority        servers.AuthorityMetadata
	RuntimeID        string
	OperationID      *string
	Generation       uint64
	DrainEpoch       uint64
	OAuthReplayStage downstream.OAuthChallengeStage
}

type Outcome struct {
	State           contract.RuntimeState
	CredentialState contract.ServerCredentialState
	CatalogState    contract.ActiveCatalogState
	Reason          *contract.PublicReason
	Retryable       bool
	OAuthChallenge  *downstream.OAuthChallengeDisposition
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
	Repository   Repository
	Driver       Driver
	Authority    AuthorityResolver
	Catalog      CatalogCoordinator
	Credentials  CredentialLifecycle
	OAuthRefresh OAuthChallengeRefresher
	OAuthStepUp  OAuthStepUpper
	Scheduler    Scheduler
	Invalidate   func(contract.Invalidation)
	Publisher    ActivePublisher
}

type DrainResult struct {
	Verified    int
	Unconfirmed int
}

type Manager struct {
	mu           sync.Mutex
	repository   Repository
	driver       Driver
	authority    AuthorityResolver
	catalog      CatalogCoordinator
	credentials  CredentialLifecycle
	oauthRefresh OAuthChallengeRefresher
	oauthStepUp  OAuthStepUpper
	scheduler    Scheduler
	invalidate   func(contract.Invalidation)
	publisher    ActivePublisher
	ctx          context.Context
	cancel       context.CancelFunc
	entries      map[string]*entry
	globalInUse  int64
	globalLimit  int64
	drainEpoch   uint64
	draining     bool
	drainDone    chan DrainResult
	workers      sync.WaitGroup
}

type entry struct {
	generation         uint64
	activating         *Candidate
	active             *Candidate
	blockedStop        *Candidate
	runtimeFailure     *FailureDisposition
	catalogHandoff     *CandidateKey
	handoffOperationID *string
	running            bool
	pending            bool
	operationID        *string
	timer              Timer
	timerVersion       uint64
	retryAttempt       int
	status             Status
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
}

func newMemoryPublisher() *memoryPublisher {
	return &memoryPublisher{fences: make(map[string]uint64)}
}

func (publisher *memoryPublisher) Fence(serverID string, generation uint64) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if generation > publisher.fences[serverID] {
		publisher.fences[serverID] = generation
	}
}

func (*memoryPublisher) Withdraw(Candidate) {}

type unavailableDriver struct{}

func (unavailableDriver) Reconcile(context.Context, Candidate, *MaterialLease) Outcome {
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
	return &Manager{repository: options.Repository, driver: options.Driver, authority: options.Authority, catalog: options.Catalog, credentials: options.Credentials, oauthRefresh: options.OAuthRefresh, oauthStepUp: options.OAuthStepUp, scheduler: options.Scheduler, invalidate: options.Invalidate, publisher: options.Publisher, ctx: ctx, cancel: cancel, entries: make(map[string]*entry), globalLimit: limit.Maximum}, nil
}

func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.repository.InterruptNonterminal(ctx); err != nil {
		return err
	}
	manager.publish(contract.InvalidationServerOperations, nil)
	manager.publish(contract.InvalidationSystemStatus, nil)
	var cursor *servers.SnapshotCursor
	all := make([]servers.Server, 0)
	for {
		page, err := manager.repository.ListServers(ctx, cursor, contract.S2ListPageDefault)
		if err != nil {
			return err
		}
		all = append(all, page.Items...)
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	for _, server := range all {
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
	return nil
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

func (manager *Manager) SetOAuthChallengeRefresher(refresher OAuthChallengeRefresher) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.oauthRefresh = refresher
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
	if operationID != nil {
		operation, err := manager.repository.GetOperation(context.Background(), *operationID)
		refresher, supported := manager.catalog.(CatalogRefresher)
		if err == nil && supported && operation.ServerID == serverID && operation.Kind == contract.OperationRefreshCatalog {
			manager.triggerCatalogRefresh(serverID, *operationID, refresher)
			return
		}
	}
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

func (manager *Manager) triggerCatalogRefresh(serverID, operationID string, refresher CatalogRefresher) {
	manager.mu.Lock()
	if manager.draining {
		manager.mu.Unlock()
		return
	}
	current := manager.entries[serverID]
	if current == nil || current.active == nil || current.running || current.pending {
		manager.mu.Unlock()
		reason := contract.ReasonSuperseded
		_, _ = manager.repository.TransitionOperation(context.Background(), operationID, contract.OperationFailed, &reason)
		manager.publish(contract.InvalidationServerOperations, &operationID)
		return
	}
	candidate := *cloneCandidate(current.active)
	candidate.OperationID = &operationID
	generation := current.generation
	current.status.CatalogState = contract.ActiveCatalogRefreshing
	manager.publish(contract.InvalidationServers, &serverID)
	manager.mu.Unlock()
	go manager.refreshCatalogOperation(serverID, generation, operationID, candidate, refresher)
}

func (manager *Manager) refreshCatalogOperation(serverID string, generation uint64, operationID string, candidate Candidate, refresher CatalogRefresher) {
	if _, err := manager.transitionCurrent(serverID, generation, operationID, contract.OperationRunning, nil); err != nil {
		return
	}
	outcome := refresher.Refresh(manager.ctx, candidate)
	if outcome.OAuthChallenge != nil {
		if !manager.HandleCatalogCompletion(candidate, outcome, &operationID) {
			reason := contract.ReasonSuperseded
			if _, err := manager.repository.TransitionOperation(context.Background(), operationID, contract.OperationSuperseded, &reason); err == nil {
				manager.publish(contract.InvalidationServerOperations, &operationID)
			}
		}
		return
	}
	if outcome.RuntimeFailure != nil {
		manager.ReportRuntimeFailure(candidate, *outcome.RuntimeFailure)
	}
	if outcome.Phase == CatalogPublicationDurableOnly {
		if outcome.Cause != CatalogPostCommitDrain {
			manager.publish(contract.InvalidationCatalog, &serverID)
		}
		if outcome.Cause == CatalogPostCommitStale {
			reason := contract.ReasonSuperseded
			if outcome.Reason != nil {
				reason = *outcome.Reason
			}
			if _, err := manager.repository.TransitionOperation(context.Background(), operationID, contract.OperationSuperseded, &reason); err == nil {
				manager.publish(contract.InvalidationServerOperations, &operationID)
			}
		}
		return
	}
	manager.mu.Lock()
	current := manager.entries[serverID]
	stillCurrent := current != nil && current.generation == generation && current.active != nil && current.active.Key() == candidate.Key() && !manager.draining
	if stillCurrent {
		current.status.CatalogState = outcome.State
		manager.publish(contract.InvalidationServers, &serverID)
	}
	manager.mu.Unlock()
	state := contract.OperationSucceeded
	if !stillCurrent || outcome.State != contract.ActiveCatalogCurrent {
		state = contract.OperationFailed
	}
	if _, err := manager.repository.TransitionOperation(context.Background(), operationID, state, outcome.Reason); err == nil {
		manager.publish(contract.InvalidationServerOperations, &operationID)
	}
}

func (manager *Manager) HandleCatalogCompletion(candidate Candidate, outcome CatalogOutcome, operationID *string) bool {
	if outcome.OAuthChallenge == nil {
		return false
	}
	manager.mu.Lock()
	current := manager.entries[candidate.Server.ID]
	if manager.draining || current == nil || current.generation != candidate.Generation || current.pending || current.active == nil || current.active.Key() != candidate.Key() {
		manager.mu.Unlock()
		return false
	}
	if current.running {
		duplicate := current.catalogHandoff != nil && *current.catalogHandoff == candidate.Key() && sameOptionalString(current.handoffOperationID, operationID)
		manager.mu.Unlock()
		return duplicate
	}
	if manager.globalInUse >= manager.globalLimit {
		manager.mu.Unlock()
		return false
	}
	key := candidate.Key()
	current.catalogHandoff = &key
	current.handoffOperationID = cloneString(operationID)
	current.running = true
	manager.globalInUse++
	current.status.Reconciliation = contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}
	current.status.CatalogState = contract.ActiveCatalogRefreshing
	manager.publish(contract.InvalidationServers, &candidate.Server.ID)
	manager.publish(contract.InvalidationSystemStatus, nil)
	manager.mu.Unlock()
	attached := cloneString(operationID)
	go manager.catalogChallengeHandoff(candidate, outcome.OAuthChallenge, attached)
	return true
}

func (manager *Manager) catalogChallengeHandoff(candidate Candidate, challenge *downstream.OAuthChallengeDisposition, operationID *string) {
	serverID := candidate.Server.ID
	generation := candidate.Generation
	if challenge.Kind == downstream.OAuthChallengeStepUp {
		manager.stageOAuthStepUp(candidate, challenge)
		manager.finishCatalogChallengeFailure(candidate, operationID, contract.ReasonAuthenticationRejected)
		return
	}
	refresher := manager.oauthChallengeRefresher()
	if refresher == nil || challenge.Kind != downstream.OAuthChallengeRefresh || challenge.Stage != downstream.OAuthChallengeCatalogFirstPage {
		manager.finishCatalogChallengeFailure(candidate, operationID, contract.ReasonAuthenticationRejected)
		return
	}
	refresh, err := refresher.RefreshOAuthChallenge(manager.ctx, oauthChallengeRefreshRequest(candidate, challenge))
	if err != nil || refresh.OAuthTokensRevision == "" || refresh.OAuthTokensRevision == candidate.Authority.CredentialRevisions.OAuthTokens {
		manager.finishCatalogChallengeFailure(candidate, operationID, contract.ReasonAuthenticationRejected)
		return
	}
	if !manager.catalogHandoffCurrent(candidate) {
		manager.stopStaleCatalogHandoff(candidate, operationID, false)
		return
	}
	if !manager.stopCandidate(candidate) {
		manager.rememberBlockedStop(serverID, candidate)
		manager.clearActiveCandidate(candidate)
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogUnavailable, contract.ReasonStopUnconfirmed, false)
		return
	}
	if !manager.markCatalogHandoffStopped(candidate) {
		manager.stopStaleCatalogHandoff(candidate, operationID, true)
		return
	}
	currentServer, serverErr := manager.repository.Get(manager.ctx, serverID)
	currentAuthority, authorityErr := manager.repository.Authority(manager.ctx, serverID)
	if serverErr != nil || authorityErr != nil || !oauthRefreshFenceCurrent(candidate, currentServer, currentAuthority, refresh) || !manager.Current(candidate) {
		manager.stopStaleCatalogHandoff(candidate, operationID, true)
		return
	}
	freshRuntimeID, idErr := manager.repository.NewID()
	if idErr != nil {
		manager.clearActivating(candidate)
		manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogUnavailable, contract.ReasonResourceLimit, true)
		return
	}
	replacement := Candidate{Server: currentServer, Authority: currentAuthority, RuntimeID: freshRuntimeID, OperationID: cloneString(operationID), Generation: generation, DrainEpoch: candidate.DrainEpoch, OAuthReplayStage: downstream.OAuthChallengeCatalogFirstPage}
	if !manager.replaceActivating(candidate, replacement) {
		manager.stopStaleCatalogHandoff(candidate, operationID, true)
		return
	}
	manager.activateCurrentCandidate(serverID, generation, operationID, currentServer, currentAuthority, replacement, true, true)
}

func (manager *Manager) clearActiveCandidate(candidate Candidate) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if current != nil && current.active != nil && current.active.Key() == candidate.Key() {
		current.active = nil
	}
}

func (manager *Manager) markCatalogHandoffStopped(candidate Candidate) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if manager.draining || current == nil || current.generation != candidate.Generation || !current.running || current.active == nil || current.active.Key() != candidate.Key() || current.activating != nil || manager.drainEpoch != candidate.DrainEpoch {
		return false
	}
	current.active = nil
	current.activating = cloneCandidate(&candidate)
	current.status.State = contract.RuntimeActivating
	current.status.RuntimeID = nil
	current.runtimeFailure = nil
	return true
}

func (manager *Manager) catalogHandoffCurrent(candidate Candidate) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	return !manager.draining && current != nil && current.generation == candidate.Generation && current.running && current.active != nil && current.active.Key() == candidate.Key() && manager.drainEpoch == candidate.DrainEpoch
}

func (manager *Manager) finishCatalogChallengeFailure(candidate Candidate, operationID *string, reason contract.PublicReason) {
	if !manager.catalogHandoffCurrent(candidate) {
		manager.stopStaleCatalogHandoff(candidate, operationID, false)
		return
	}
	if !manager.stopCandidate(candidate) {
		manager.rememberBlockedStop(candidate.Server.ID, candidate)
		reason = contract.ReasonStopUnconfirmed
	}
	manager.mu.Lock()
	current := manager.entries[candidate.Server.ID]
	if current != nil && current.active != nil && current.active.Key() == candidate.Key() {
		current.active = nil
	}
	manager.mu.Unlock()
	if !manager.generationCurrent(candidate.Server.ID, candidate.Generation) {
		manager.stopStaleCatalogHandoff(candidate, operationID, true)
		return
	}
	state := contract.RuntimeAuthenticationRequired
	if reason == contract.ReasonStopUnconfirmed {
		state = contract.RuntimeDegraded
	}
	manager.finishFailure(candidate.Server.ID, candidate.Generation, operationID, state, contract.ServerCredentialUnavailable, contract.ActiveCatalogUnavailable, reason, false)
}

func (manager *Manager) stopStaleCatalogHandoff(candidate Candidate, operationID *string, stopped bool) {
	if !stopped && !manager.Current(candidate) && !manager.stopCandidate(candidate) {
		manager.rememberBlockedStop(candidate.Server.ID, candidate)
	}
	manager.mu.Lock()
	current := manager.entries[candidate.Server.ID]
	if current != nil {
		if current.active != nil && current.active.Key() == candidate.Key() {
			current.active = nil
		}
		if current.activating != nil && current.activating.Key() == candidate.Key() {
			current.activating = nil
		}
	}
	manager.mu.Unlock()
	if operationID != nil {
		manager.transitionSupersededUnlessDraining(*operationID)
	}
	manager.finishStale(candidate.Server.ID, candidate.Generation)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
		manager.workers.Add(1)
		go func() {
			defer manager.workers.Done()
			manager.reconcile(serverID, generation, operationID)
		}()
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
	if server.DesiredState != contract.DesiredServerDeleted {
		if _, err := servers.DecodeTransport(server.Transport); err != nil {
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonConfigurationInvalid, false)
			return
		}
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
			durableState := contract.DurableCatalogUnavailable
			activeState := contract.ActiveCatalogUnavailable
			if server.DesiredState == contract.DesiredServerDeleted {
				state = contract.RuntimeDeleted
				durableState = contract.DurableCatalogRetired
				activeState = contract.ActiveCatalogAbsent
			}
			if credentialOutcome.CleanupPending {
				manager.finishFailure(serverID, generation, operationID, state, contract.ServerCredentialCleanupPending, activeState, contract.ReasonCleanupPending, false)
				return
			}
			if !manager.finalizeCatalogLifecycle(server, durableState, activeState) {
				manager.finishFailure(serverID, generation, operationID, state, credentialOutcome.CredentialState, activeState, contract.ReasonConnectivity, false)
				return
			}
			manager.finishSuccess(serverID, generation, operationID, Outcome{State: state, CredentialState: credentialOutcome.CredentialState, CatalogState: activeState, Reason: credentialOutcome.Reason}, nil)
			return
		}
	}
	if server.DesiredState != contract.DesiredServerEnabled {
		state := contract.RuntimeInactive
		durableState := contract.DurableCatalogStale
		if server.DesiredState == contract.DesiredServerDeleted {
			state = contract.RuntimeDeleted
			durableState = contract.DurableCatalogRetired
		}
		finalizeLifecycle := operation != nil && (operation.Kind == contract.OperationDisable || operation.Kind == contract.OperationDelete)
		if finalizeLifecycle && !manager.finalizeCatalogLifecycle(server, durableState, contract.ActiveCatalogAbsent) {
			manager.finishFailure(serverID, generation, operationID, state, contract.ServerCredentialNotRequired, contract.ActiveCatalogAbsent, contract.ReasonConnectivity, false)
			return
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
	if !manager.recordActivating(candidate) {
		manager.finishStale(serverID, generation)
		return
	}
	manager.activateCurrentCandidate(serverID, generation, operationID, server, authority, candidate, false, false)
}

func (manager *Manager) activateCurrentCandidate(serverID string, generation uint64, operationID *string, server servers.Server, authority servers.AuthorityMetadata, candidate Candidate, challengeConsumed, catalogHandoff bool) {
	defer func() { manager.clearActivating(candidate) }()
	var outcome Outcome
	started := false
	for {
		authorityOutcome := manager.authority.Resolve(manager.ctx, candidate)
		outcome = Outcome{State: authorityOutcome.State, CredentialState: authorityOutcome.CredentialState, CatalogState: contract.ActiveCatalogAbsent, Reason: authorityOutcome.Reason, Retryable: authorityOutcome.Retryable}
		started = false
		if authorityOutcome.State == "" {
			if !manager.Current(candidate) {
				if authorityOutcome.Lease != nil {
					authorityOutcome.Lease.Clear()
				}
				manager.finishStale(serverID, generation)
				return
			}
			outcome = manager.driver.Reconcile(manager.ctx, candidate, authorityOutcome.Lease)
			started = true
			if outcome.CredentialState == "" {
				outcome.CredentialState = authorityOutcome.CredentialState
			}
			if outcome.State == contract.RuntimeActive {
				catalog := manager.catalog.Activate(manager.ctx, candidate)
				outcome.CatalogState = catalog.State
				outcome.OAuthChallenge = catalog.OAuthChallenge
				if outcome.CatalogState == "" {
					outcome.CatalogState = contract.ActiveCatalogAbsent
				}
				if catalog.Reason != nil {
					outcome.Reason = catalog.Reason
				}
				if catalog.RuntimeFailure != nil {
					outcome.State = catalog.RuntimeFailure.State
					outcome.Reason = &catalog.RuntimeFailure.Reason
					outcome.Retryable = catalog.RuntimeFailure.Retryable
					outcome.CatalogState = contract.ActiveCatalogUnavailable
					if catalog.RuntimeFailure.State == contract.RuntimeAuthenticationRequired {
						outcome.CredentialState = contract.ServerCredentialUnavailable
					}
				}
				if catalog.Phase == CatalogPublicationDurableOnly {
					if authorityOutcome.Lease != nil {
						authorityOutcome.Lease.Clear()
					}
					manager.finishDurableOnly(serverID, generation, operationID, candidate, catalog, outcome.CredentialState)
					return
				}
			}
		}
		if authorityOutcome.Lease != nil {
			authorityOutcome.Lease.Clear()
		}
		challenge := outcome.OAuthChallenge
		if challenge == nil {
			break
		}
		reason := contract.ReasonAuthenticationRejected
		outcome.State = contract.RuntimeAuthenticationRequired
		outcome.CredentialState = contract.ServerCredentialUnavailable
		outcome.Reason = &reason
		outcome.Retryable = false
		if challenge.Kind == downstream.OAuthChallengeStepUp {
			manager.stageOAuthStepUp(candidate, challenge)
			break
		}
		refresher := manager.oauthChallengeRefresher()
		if challengeConsumed || refresher == nil || challenge.Kind != downstream.OAuthChallengeRefresh || !replayableOAuthStage(challenge.Stage) {
			break
		}
		challengeConsumed = true
		refresh, refreshErr := refresher.RefreshOAuthChallenge(manager.ctx, oauthChallengeRefreshRequest(candidate, challenge))
		if refreshErr != nil || refresh.OAuthTokensRevision == "" || refresh.OAuthTokensRevision == candidate.Authority.CredentialRevisions.OAuthTokens {
			break
		}
		if !manager.Current(candidate) {
			if started && !manager.stopCandidate(candidate) {
				manager.rememberBlockedStop(serverID, candidate)
			}
			manager.finishStale(serverID, generation)
			return
		}
		if started && !manager.stopCandidate(candidate) {
			manager.rememberBlockedStop(serverID, candidate)
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, outcome.CredentialState, outcome.CatalogState, contract.ReasonStopUnconfirmed, false)
			return
		}
		currentServer, serverErr := manager.repository.Get(manager.ctx, serverID)
		currentAuthority, authorityErr := manager.repository.Authority(manager.ctx, serverID)
		if serverErr != nil || authorityErr != nil || !oauthRefreshFenceCurrent(candidate, currentServer, currentAuthority, refresh) || !manager.current(serverID, generation, candidate.DrainEpoch) {
			manager.finishStale(serverID, generation)
			return
		}
		freshRuntimeID, idErr := manager.repository.NewID()
		if idErr != nil {
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, contract.ServerCredentialUnavailable, contract.ActiveCatalogAbsent, contract.ReasonResourceLimit, true)
			return
		}
		replacement := Candidate{Server: currentServer, Authority: currentAuthority, RuntimeID: freshRuntimeID, OperationID: cloneString(operationID), Generation: generation, DrainEpoch: candidate.DrainEpoch, OAuthReplayStage: challenge.Stage}
		if !manager.replaceActivating(candidate, replacement) {
			manager.finishStale(serverID, generation)
			return
		}
		candidate = replacement
		server = currentServer
		authority = currentAuthority
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
	if catalogHandoff && operationID != nil && outcome.CatalogState != contract.ActiveCatalogCurrent {
		manager.finishWithOperationState(serverID, generation, operationID, contract.OperationFailed, outcome, &candidate)
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
	if current != nil && current.blockedStop != nil && current.blockedStop.Key() == previous.Key() {
		current.blockedStop = nil
	}
	manager.mu.Unlock()
	return true
}

func (manager *Manager) abandonCandidate(candidate Candidate) {
	if abandoner, ok := manager.catalog.(CatalogAbandoner); ok {
		abandoner.Abandon(candidate)
	}
	manager.publisher.Withdraw(candidate)
}

func (manager *Manager) withdrawCandidate(candidate Candidate) {
	if refresher, ok := manager.catalog.(CatalogRefresher); ok {
		refresher.Withdraw(candidate, contract.ActiveCatalogUnavailable)
	}
	manager.publisher.Withdraw(candidate)
}

func (manager *Manager) stopCandidate(candidate Candidate) bool {
	manager.withdrawCandidate(candidate)
	return manager.driver.Stop(context.Background(), candidate)
}

func (manager *Manager) finalizeCatalogLifecycle(server servers.Server, durableState contract.DurableCatalogState, activeState contract.ActiveCatalogState) bool {
	finalizer, ok := manager.catalog.(CatalogLifecycleFinalizer)
	if !ok {
		return true
	}
	authority, err := manager.repository.Authority(manager.ctx, server.ID)
	if err != nil {
		return false
	}
	return finalizer.FinalizeLifecycle(manager.ctx, server, authority, durableState, activeState) == nil
}

func (manager *Manager) rememberBlockedStop(serverID string, candidate Candidate) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entryLocked(serverID)
	current.blockedStop = cloneCandidate(&candidate)
}

func (manager *Manager) stageOAuthStepUp(candidate Candidate, challenge *downstream.OAuthChallengeDisposition) {
	manager.mu.Lock()
	stepUp := manager.oauthStepUp
	manager.mu.Unlock()
	if stepUp != nil {
		_ = stepUp.StageStepUp(candidate.Server.ID, challenge.Metadata, nil, challenge.Scopes)
	}
}

func (manager *Manager) oauthChallengeRefresher() OAuthChallengeRefresher {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.oauthRefresh
}

func replayableOAuthStage(stage downstream.OAuthChallengeStage) bool {
	return stage == downstream.OAuthChallengeModernDiscovery || stage == downstream.OAuthChallengeLegacyInitialize || stage == downstream.OAuthChallengeCatalogFirstPage
}

func oauthChallengeRefreshRequest(candidate Candidate, challenge *downstream.OAuthChallengeDisposition) OAuthChallengeRefreshRequest {
	return OAuthChallengeRefreshRequest{
		ServerID: candidate.Server.ID, ExpectedDesiredRevision: candidate.Server.DesiredRevision,
		ExpectedRegistrationRevision: candidate.Authority.RegistrationRevision,
		ExpectedOAuthClientRevision:  candidate.Authority.CredentialRevisions.OAuthClient,
		ExpectedOAuthTokensRevision:  candidate.Authority.CredentialRevisions.OAuthTokens,
		ChallengeMetadata:            append([]string(nil), challenge.Metadata...),
	}
}

func oauthRefreshFenceCurrent(candidate Candidate, currentServer servers.Server, currentAuthority servers.AuthorityMetadata, refresh OAuthChallengeRefreshResult) bool {
	return candidate.Server.ID == currentServer.ID && candidate.Server.DesiredRevision == currentServer.DesiredRevision && candidate.Server.DesiredState == currentServer.DesiredState && bytes.Equal(candidate.Server.Transport, currentServer.Transport) &&
		candidate.Authority.RegistrationRevision == currentAuthority.RegistrationRevision && candidate.Authority.CredentialRevisions.StaticCredential == currentAuthority.CredentialRevisions.StaticCredential && candidate.Authority.CredentialRevisions.OAuthClient == currentAuthority.CredentialRevisions.OAuthClient &&
		refresh.OAuthTokensRevision == currentAuthority.CredentialRevisions.OAuthTokens
}

func sameFence(server servers.Server, authority servers.AuthorityMetadata, current servers.Server, currentAuthority servers.AuthorityMetadata) bool {
	return server.DesiredState == current.DesiredState && bytes.Equal(server.Transport, current.Transport) && authority.RegistrationRevision == currentAuthority.RegistrationRevision && authority.CredentialRevisions == currentAuthority.CredentialRevisions
}

func (manager *Manager) replaceActivating(previous, replacement Candidate) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[previous.Server.ID]
	if current == nil || current.generation != previous.Generation || manager.draining || manager.drainEpoch != previous.DrainEpoch || current.activating == nil || current.activating.Key() != previous.Key() {
		return false
	}
	current.activating = cloneCandidate(&replacement)
	current.runtimeFailure = nil
	return true
}

func (manager *Manager) Current(candidate Candidate) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if current == nil || current.generation != candidate.Generation || manager.draining {
		return false
	}
	key := candidate.Key()
	return current.activating != nil && current.activating.Key() == key || current.active != nil && current.active.Key() == key
}

func (manager *Manager) recordActivating(candidate Candidate) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if current == nil || current.generation != candidate.Generation || manager.drainEpoch != candidate.DrainEpoch || manager.draining || current.activating != nil {
		return false
	}
	current.activating = cloneCandidate(&candidate)
	current.runtimeFailure = nil
	return true
}

func (manager *Manager) clearActivating(candidate Candidate) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if current != nil && current.activating != nil && current.activating.Key() == candidate.Key() {
		current.activating = nil
		current.runtimeFailure = nil
	}
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

func (manager *Manager) finishDurableOnly(serverID string, generation uint64, operationID *string, candidate Candidate, catalog CatalogOutcome, credentialState contract.ServerCredentialState) {
	reason := contract.ReasonConnectivity
	switch catalog.Cause {
	case CatalogPostCommitStale:
		reason = contract.ReasonSuperseded
	case CatalogPostCommitDrain:
		reason = contract.ReasonInterrupted
	}
	if catalog.Reason != nil {
		reason = *catalog.Reason
	}
	if catalog.Cause != CatalogPostCommitDrain {
		manager.publish(contract.InvalidationCatalog, &serverID)
	}
	if catalog.Cause == CatalogPostCommitStale && operationID != nil {
		if _, err := manager.repository.TransitionOperation(context.Background(), *operationID, contract.OperationSuperseded, &reason); err == nil {
			manager.publish(contract.InvalidationServerOperations, operationID)
		}
	}
	manager.abandonCandidate(candidate)
	stopped := manager.driver.Stop(context.Background(), candidate)

	manager.mu.Lock()
	current := manager.entries[serverID]
	if current != nil {
		if !stopped {
			current.blockedStop = cloneCandidate(&candidate)
			current.pending = false
			stopReason := contract.ReasonStopUnconfirmed
			current.status.State = contract.RuntimeDegraded
			current.status.Reason = &stopReason
			current.status.CredentialState = credentialState
			current.status.CatalogState = contract.ActiveCatalogAbsent
			current.status.RuntimeID = nil
		} else if catalog.Cause == CatalogPostCommitStorage && current.generation == generation && !manager.draining {
			current.status.State = contract.RuntimeDegraded
			current.status.Reason = &reason
			current.status.CredentialState = credentialState
			current.status.CatalogState = contract.ActiveCatalogAbsent
			current.status.RuntimeID = nil
		}
	}
	manager.releaseLocked(current)
	manager.mu.Unlock()
	if !stopped && catalog.Cause != CatalogPostCommitDrain {
		manager.publish(contract.InvalidationServers, &serverID)
		manager.publish(contract.InvalidationSystemStatus, nil)
	}
}

func (manager *Manager) finishSuccess(serverID string, generation uint64, operationID *string, outcome Outcome, candidate *Candidate) {
	manager.finishWithOperationState(serverID, generation, operationID, contract.OperationSucceeded, outcome, candidate)
}

func (manager *Manager) finishWithOperationState(serverID string, generation uint64, operationID *string, operationState contract.ServerOperationState, outcome Outcome, candidate *Candidate) {
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
	if candidate != nil && current.runtimeFailure != nil {
		failure := *current.runtimeFailure
		current.runtimeFailure = nil
		manager.mu.Unlock()
		if !manager.stopCandidate(*candidate) {
			manager.rememberBlockedStop(serverID, *candidate)
			manager.finishFailure(serverID, generation, operationID, contract.RuntimeDegraded, outcome.CredentialState, outcome.CatalogState, contract.ReasonStopUnconfirmed, false)
			return
		}
		state := failure.State
		if failure.Retryable {
			state = contract.RuntimeRetryWait
		}
		credentialState := outcome.CredentialState
		if failure.State == contract.RuntimeAuthenticationRequired {
			credentialState = contract.ServerCredentialUnavailable
		}
		manager.finishFailure(serverID, generation, operationID, state, credentialState, contract.ActiveCatalogUnavailable, failure.Reason, failure.Retryable)
		return
	}
	if candidate != nil && outcome.State == contract.RuntimeActive {
		current.active = cloneCandidate(candidate)
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
	manager.mu.Unlock()

	manager.publish(contract.InvalidationCatalog, &serverID)
	manager.publish(contract.InvalidationServers, &serverID)

	manager.mu.Lock()
	current = manager.entries[serverID]
	if current == nil || current.generation != generation || manager.draining {
		if current != nil && candidate != nil && current.active != nil && current.active.Key() == candidate.Key() {
			current.active = nil
			current.status.RuntimeID = nil
		}
		manager.mu.Unlock()
		if candidate != nil {
			if !manager.stopCandidate(*candidate) {
				manager.rememberBlockedStop(serverID, *candidate)
			}
		}
		manager.finishStale(serverID, generation)
		return
	}
	if operationID != nil {
		if _, err := manager.repository.TransitionOperation(context.Background(), *operationID, operationState, outcome.Reason); err != nil {
			current.generation++
			manager.publisher.Fence(serverID, current.generation)
			current.active = nil
			current.status.State = contract.RuntimeDegraded
			reason := contract.ReasonConnectivity
			current.status.Reason = &reason
			current.status.CatalogState = contract.ActiveCatalogUnavailable
			current.status.RuntimeID = nil
			manager.releaseLocked(current)
			manager.mu.Unlock()
			if candidate != nil {
				manager.withdrawCandidate(*candidate)
			}
			manager.publish(contract.InvalidationCatalog, &serverID)
			manager.publish(contract.InvalidationServers, &serverID)
			manager.publish(contract.InvalidationSystemStatus, nil)
			if candidate != nil && !manager.driver.Stop(context.Background(), *candidate) {
				manager.mu.Lock()
				current = manager.entryLocked(serverID)
				current.blockedStop = cloneCandidate(candidate)
				stopReason := contract.ReasonStopUnconfirmed
				current.status.State = contract.RuntimeDegraded
				current.status.Reason = &stopReason
				current.status.CatalogState = contract.ActiveCatalogUnavailable
				current.status.RuntimeID = nil
				manager.mu.Unlock()
				manager.publish(contract.InvalidationServers, &serverID)
				manager.publish(contract.InvalidationSystemStatus, nil)
			}
			return
		}
	}
	manager.releaseLocked(current)
	manager.mu.Unlock()
	if operationID != nil {
		manager.publish(contract.InvalidationServerOperations, operationID)
	}
	manager.publish(contract.InvalidationSystemStatus, nil)
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

func (manager *Manager) transitionSupersededUnlessDraining(operationID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining {
		return
	}
	reason := contract.ReasonSuperseded
	if _, err := manager.repository.TransitionOperation(context.Background(), operationID, contract.OperationSuperseded, &reason); err == nil {
		manager.publish(contract.InvalidationServerOperations, &operationID)
	}
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
		current.catalogHandoff = nil
		current.handoffOperationID = nil
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

func (manager *Manager) RuntimeFailed(candidate Candidate, reason contract.PublicReason) bool {
	return manager.ReportRuntimeFailure(candidate, FailureDisposition{State: contract.RuntimeDegraded, Reason: reason, Retryable: true, RuntimeLost: true})
}

func (manager *Manager) ReportRuntimeFailure(candidate Candidate, failure FailureDisposition) bool {
	if !validRuntimeFailure(failure) {
		return false
	}
	manager.mu.Lock()
	serverID := candidate.Server.ID
	current := manager.entries[serverID]
	if current == nil || manager.draining {
		manager.mu.Unlock()
		return false
	}
	if current.active == nil || current.active.Key() != candidate.Key() {
		if current.activating == nil || current.activating.Key() != candidate.Key() || current.runtimeFailure != nil {
			manager.mu.Unlock()
			return false
		}
		cloned := failure
		current.runtimeFailure = &cloned
		manager.mu.Unlock()
		return true
	}
	failed := cloneCandidate(current.active)
	current.active = nil
	current.generation++
	generation := current.generation
	manager.publisher.Fence(serverID, generation)
	current.status.State = contract.RuntimeStopping
	current.status.Reason = &failure.Reason
	current.status.RuntimeID = nil
	current.status.CatalogState = contract.ActiveCatalogUnavailable
	if failure.State == contract.RuntimeAuthenticationRequired {
		current.status.CredentialState = contract.ServerCredentialUnavailable
	}
	manager.publish(contract.InvalidationServers, &serverID)
	manager.publish(contract.InvalidationSystemStatus, nil)
	manager.mu.Unlock()
	go manager.finishRuntimeFailure(serverID, generation, *failed, failure)
	return true
}

func (manager *Manager) finishRuntimeFailure(serverID string, generation uint64, failed Candidate, failure FailureDisposition) {
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
	current.status.State = failure.State
	current.status.Reason = &failure.Reason
	if failure.Retryable {
		current.status.State = contract.RuntimeRetryWait
		manager.scheduleRetryLocked(serverID, current)
	}
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
	type drainCandidate struct {
		candidate Candidate
		stop      bool
	}
	candidates := make([]drainCandidate, 0, len(manager.entries))
	seen := make(map[string]int, len(manager.entries))
	for serverID, current := range manager.entries {
		current.generation++
		manager.publisher.Fence(serverID, current.generation)
		current.pending = false
		if current.timer != nil {
			current.timer.Stop()
			current.timer = nil
		}
		owned := current.activating != nil || current.active != nil || current.blockedStop != nil
		for index, candidate := range []*Candidate{current.activating, current.active, current.blockedStop} {
			if candidate == nil {
				continue
			}
			manager.publisher.Withdraw(*candidate)
			stop := index != 0
			if existing, duplicate := seen[candidate.RuntimeID]; duplicate {
				candidates[existing].stop = candidates[existing].stop || stop
			} else {
				seen[candidate.RuntimeID] = len(candidates)
				candidates = append(candidates, drainCandidate{candidate: *candidate, stop: stop})
			}
		}
		current.activating = nil
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
		type stopResult struct {
			candidate Candidate
			verified  bool
		}
		results := make(chan stopResult, len(candidates))
		for _, candidate := range candidates {
			candidate := candidate
			go func() {
				verified := false
				if candidate.stop {
					verified = manager.driver.Stop(ctx, candidate.candidate)
				}
				results <- stopResult{candidate: candidate.candidate, verified: verified}
			}()
		}
		stops := make([]stopResult, 0, len(candidates))
		for range candidates {
			stops = append(stops, <-results)
		}
		ownership, observesOwnership := manager.driver.(interface{ Owned(Candidate) bool })
		result := DrainResult{}
		for _, stopped := range stops {
			verified := stopped.verified
			if observesOwnership && !ownership.Owned(stopped.candidate) {
				verified = true
			}
			if verified {
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

func (manager *Manager) Wait(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		manager.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (manager *Manager) Shutdown() {
	if catalog, ok := manager.catalog.(interface{ Shutdown() }); ok {
		catalog.Shutdown()
	}
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
