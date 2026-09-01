package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

const (
	catalogPollPeriod = 5 * time.Minute
	catalogPollWindow = 30 * time.Second
)

type ClientProvider func(runtimes.Candidate) (PageClient, bool)
type RuntimeCurrent func(runtimes.Candidate) bool

type CoordinatorOptions struct {
	InstallationID string
	Repository     *Repository
	Active         *ActiveRegistry
	Traverser      *Traverser
	Clock          Clock
	Scheduler      runtimes.Scheduler
	Client         ClientProvider
	Current        RuntimeCurrent
	Complete       func(runtimes.Candidate, runtimes.CatalogOutcome, *string)
}

type refreshWork struct {
	done        chan struct{}
	cancel      context.CancelFunc
	candidate   runtimes.Candidate
	operationID *string
	result      runtimes.CatalogOutcome
}

type Coordinator struct {
	installationID string
	repository     *Repository
	active         *ActiveRegistry
	traverser      *Traverser
	clock          Clock
	scheduler      runtimes.Scheduler
	client         ClientProvider
	current        RuntimeCurrent
	complete       func(runtimes.Candidate, runtimes.CatalogOutcome, *string)
	ctx            context.Context
	cancel         context.CancelFunc

	mu           sync.Mutex
	work         map[string]*refreshWork
	timers       map[string]runtimes.Timer
	candidates   map[string]runtimes.Candidate
	stopped      bool
	workers      sync.WaitGroup
	shutdownOnce sync.Once
	shutdownDone chan struct{}
}

func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.InstallationID == "" || options.Repository == nil || options.Active == nil || options.Traverser == nil || options.Clock == nil || options.Scheduler == nil || options.Client == nil || options.Current == nil {
		return nil, servers.ErrInvalidInput
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{installationID: options.InstallationID, repository: options.Repository, active: options.Active, traverser: options.Traverser, clock: options.Clock, scheduler: options.Scheduler, client: options.Client, current: options.Current, complete: options.Complete, ctx: ctx, cancel: cancel, work: make(map[string]*refreshWork), timers: make(map[string]runtimes.Timer), candidates: make(map[string]runtimes.Candidate), shutdownDone: make(chan struct{})}, nil
}

func PollOffset(installationID, serverID string) time.Duration {
	digest := sha256.Sum256(append(append([]byte(installationID), 0), []byte(serverID)...))
	return time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(catalogPollWindow))
}

func NextPoll(now time.Time, offset time.Duration) time.Time {
	period := int64(catalogPollPeriod)
	shifted := now.UnixNano() - int64(offset)
	quotient := shifted / period
	if shifted < 0 && shifted%period != 0 {
		quotient--
	}
	next := (quotient+1)*period + int64(offset)
	return time.Unix(0, next).UTC()
}

func (coordinator *Coordinator) Activate(ctx context.Context, candidate runtimes.Candidate) runtimes.CatalogOutcome {
	return coordinator.run(ctx, candidate, runtimes.CatalogTraversalInitial)
}

func (coordinator *Coordinator) Refresh(ctx context.Context, candidate runtimes.Candidate) runtimes.CatalogOutcome {
	return coordinator.run(ctx, candidate, runtimes.CatalogTraversalRefresh)
}

func (coordinator *Coordinator) run(ctx context.Context, candidate runtimes.Candidate, intent runtimes.CatalogTraversalIntent) runtimes.CatalogOutcome {
	serverID := candidate.Server.ID
	coordinator.mu.Lock()
	if coordinator.stopped {
		coordinator.mu.Unlock()
		return catalogOutcome(intent, contract.ActiveCatalogUnavailable, contract.ReasonInterrupted)
	}
	coordinator.workers.Add(1)
	defer coordinator.workers.Done()
	if current := coordinator.work[serverID]; current != nil {
		if current.candidate.Key() != candidate.Key() {
			coordinator.mu.Unlock()
			return catalogOutcome(intent, contract.ActiveCatalogUnavailable, contract.ReasonSuperseded)
		}
		if intent == runtimes.CatalogTraversalRefresh && candidate.OperationID != nil {
			if current.operationID != nil && *current.operationID != *candidate.OperationID {
				coordinator.mu.Unlock()
				return catalogOutcome(intent, contract.ActiveCatalogUnavailable, contract.ReasonSuperseded)
			}
			current.operationID = cloneOperationID(candidate.OperationID)
		}
		done := current.done
		coordinator.mu.Unlock()
		select {
		case <-done:
			return current.result
		case <-ctx.Done():
			return catalogOutcome(intent, contract.ActiveCatalogUnavailable, contract.ReasonCancelled)
		}
	}
	workCtx, cancel := context.WithCancel(coordinator.ctx)
	current := &refreshWork{done: make(chan struct{}), cancel: cancel, candidate: candidate}
	if intent == runtimes.CatalogTraversalRefresh {
		current.operationID = cloneOperationID(candidate.OperationID)
	}
	coordinator.work[serverID] = current
	coordinator.candidates[serverID] = candidate
	coordinator.mu.Unlock()

	result := coordinator.execute(workCtx, candidate, intent)
	cancel()
	coordinator.mu.Lock()
	current.result = result
	if coordinator.work[serverID] == current {
		delete(coordinator.work, serverID)
	}
	close(current.done)
	stopped := coordinator.stopped
	complete := coordinator.complete
	operationID := cloneOperationID(current.operationID)
	coordinator.mu.Unlock()
	if !stopped && intent == runtimes.CatalogTraversalPoll && result.OAuthChallenge != nil && complete != nil {
		complete(candidate, result, operationID)
	}
	if !stopped && result.RuntimeHealth != runtimes.CatalogRuntimeLost && result.OAuthChallenge == nil && coordinator.live(candidate) {
		coordinator.schedule(candidate)
	}
	return result
}

func (coordinator *Coordinator) Abandon(candidate runtimes.Candidate) {
	coordinator.detach(candidate)
}

func (coordinator *Coordinator) Withdraw(candidate runtimes.Candidate, state contract.ActiveCatalogState) {
	coordinator.detach(candidate)
	coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, state)
}

func (coordinator *Coordinator) FinalizeLifecycle(ctx context.Context, server servers.Server, authority servers.AuthorityMetadata, durableState contract.DurableCatalogState, activeState contract.ActiveCatalogState) error {
	return coordinator.active.FinalizeLifecycle(ctx, CommitFence{
		ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision,
		ExpectedRegistrationRevision: authority.RegistrationRevision, ExpectedCredentialRevisions: authority.CredentialRevisions,
	}, server.DesiredState, durableState, activeState)
}

func (coordinator *Coordinator) detach(candidate runtimes.Candidate) {
	coordinator.mu.Lock()
	serverID := candidate.Server.ID
	current, currentExists := coordinator.candidates[serverID]
	if currentExists && current.Key() == candidate.Key() {
		if timer := coordinator.timers[serverID]; timer != nil {
			timer.Stop()
			delete(coordinator.timers, serverID)
		}
		delete(coordinator.candidates, serverID)
	}
	if work := coordinator.work[serverID]; work != nil && work.candidate.Key() == candidate.Key() {
		work.cancel()
	}
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) Shutdown() {
	coordinator.mu.Lock()
	if !coordinator.stopped {
		coordinator.stopped = true
		coordinator.cancel()
		for _, timer := range coordinator.timers {
			timer.Stop()
		}
		for _, work := range coordinator.work {
			work.cancel()
		}
		coordinator.timers = make(map[string]runtimes.Timer)
		coordinator.candidates = make(map[string]runtimes.Candidate)
	}
	coordinator.shutdownOnce.Do(func() {
		go func() {
			coordinator.workers.Wait()
			close(coordinator.shutdownDone)
		}()
	})
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) Wait(ctx context.Context) bool {
	coordinator.Shutdown()
	select {
	case <-coordinator.shutdownDone:
		return true
	case <-ctx.Done():
		return false
	}
}

func (coordinator *Coordinator) Status() contract.LimitStatus { return coordinator.traverser.Status() }

func (coordinator *Coordinator) ServerStatus(serverID string) contract.LimitStatus {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	inUse := int64(0)
	if coordinator.work[serverID] != nil {
		inUse = 1
	}
	return contract.LimitStatus{InUse: inUse, Limit: 1, Saturated: inUse == 1}
}

func (coordinator *Coordinator) execute(ctx context.Context, candidate runtimes.Candidate, intent runtimes.CatalogTraversalIntent) runtimes.CatalogOutcome {
	if !coordinator.live(candidate) {
		coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
		return catalogOutcome(intent, contract.ActiveCatalogUnavailable, contract.ReasonSuperseded)
	}
	client, ok := coordinator.client(candidate)
	if !ok {
		failure := runtimes.ClassifyFailure(downstream.ErrUnsupportedProtocol)
		return coordinator.failure(candidate, intent, downstream.ErrUnsupportedProtocol, &failure)
	}
	raw, err := coordinator.traverser.Traverse(ctx, client, candidate.Server.Namespace)
	if err != nil {
		return coordinator.failure(candidate, intent, err, catalogRuntimeFailure(err, client))
	}
	runtime, _ := client.(*downstream.Runtime)
	normalized := NormalizeCandidate(raw, NormalizeOptions{ServerID: candidate.Server.ID, AllowHeaderBindings: allowsHeaderBindings(candidate, runtime)})
	durable, err := coordinator.repository.Status(ctx, candidate.Server.ID)
	if err != nil {
		return coordinator.failure(candidate, intent, servers.ErrStorageUnavailable, nil)
	}
	revision := "0"
	if durable.Revision != nil {
		revision = *durable.Revision
	}
	status, err := coordinator.active.Publish(ctx, Publication{
		Fence:             coordinator.commitFence(candidate, revision),
		ServerDisplayName: candidate.Server.DisplayName,
		RuntimeID:         candidate.RuntimeID, RuntimeGeneration: candidate.Generation, Candidate: normalized, Runtime: runtime,
		Current: func() bool { return coordinator.live(candidate) },
	})
	if err != nil {
		var publicationFailure *PublicationFailure
		if errors.As(err, &publicationFailure) {
			reason, cause := postCommitFailure(publicationFailure)
			return runtimes.CatalogOutcome{State: coordinator.active.Status(candidate.Server.ID).State, Reason: &reason, Phase: runtimes.CatalogPublicationDurableOnly, Cause: cause, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy}
		}
		return coordinator.failure(candidate, intent, err, nil)
	}
	return runtimes.CatalogOutcome{State: status.State, Phase: runtimes.CatalogPublicationInstalled, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy}
}

func (coordinator *Coordinator) failure(candidate runtimes.Candidate, intent runtimes.CatalogTraversalIntent, err error, runtimeFailure *runtimes.FailureDisposition) runtimes.CatalogOutcome {
	reason := catalogFailureReason(err)
	var challenge *downstream.OAuthChallengeDisposition
	if errors.As(err, &challenge) {
		reason = contract.ReasonAuthenticationRejected
	}
	live := coordinator.live(candidate)
	if challenge != nil {
		state := contract.ActiveCatalogUnavailable
		if intent != runtimes.CatalogTraversalInitial {
			state = coordinator.active.Status(candidate.Server.ID).State
			if state == contract.ActiveCatalogAbsent {
				state = contract.ActiveCatalogUnavailable
			}
		}
		return runtimes.CatalogOutcome{State: state, Reason: &reason, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy, OAuthChallenge: challenge}
	}
	if runtimeFailure != nil && runtimeFailure.RuntimeLost {
		reason = runtimeFailure.Reason
		if live {
			coordinator.setFailureState(candidate, contract.DurableCatalogUnavailable)
			coordinator.active.MarkUnavailableExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, 1)
		} else {
			coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
		}
		coordinator.detach(candidate)
		failure := *runtimeFailure
		return runtimes.CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &reason, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeLost, RuntimeFailure: &failure}
	}
	if live && intent != runtimes.CatalogTraversalInitial {
		active := coordinator.active.Status(candidate.Server.ID)
		if active.State == contract.ActiveCatalogCurrent || active.State == contract.ActiveCatalogStale {
			if coordinator.setFailureState(candidate, contract.DurableCatalogStale) && coordinator.active.MarkStaleExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, 1) {
				return runtimes.CatalogOutcome{State: contract.ActiveCatalogStale, Reason: &reason, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy, OAuthChallenge: challenge}
			}
		}
	}
	if live {
		coordinator.setFailureState(candidate, contract.DurableCatalogUnavailable)
		coordinator.active.MarkUnavailableExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, 1)
	} else {
		coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
	}
	return runtimes.CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &reason, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy, OAuthChallenge: challenge}
}

func (coordinator *Coordinator) setFailureState(candidate runtimes.Candidate, state contract.DurableCatalogState) bool {
	durable, err := coordinator.repository.Status(coordinator.ctx, candidate.Server.ID)
	if err != nil {
		return false
	}
	revision := "0"
	if durable.Revision != nil {
		revision = *durable.Revision
	}
	if durable.State == state && durable.IssueCount == 1 {
		return true
	}
	_, err = coordinator.repository.SetState(coordinator.ctx, coordinator.commitFence(candidate, revision), state, 1)
	return err == nil
}

func (coordinator *Coordinator) commitFence(candidate runtimes.Candidate, revision string) CommitFence {
	return CommitFence{ServerID: candidate.Server.ID, ExpectedDesiredRevision: candidate.Server.DesiredRevision, ExpectedRegistrationRevision: candidate.Authority.RegistrationRevision, ExpectedCredentialRevisions: candidate.Authority.CredentialRevisions, ExpectedCatalogRevision: revision}
}

func (coordinator *Coordinator) live(candidate runtimes.Candidate) bool {
	coordinator.mu.Lock()
	current, exists := coordinator.candidates[candidate.Server.ID]
	live := !coordinator.stopped && exists && current.Key() == candidate.Key()
	coordinator.mu.Unlock()
	return live && coordinator.current(candidate)
}

func (coordinator *Coordinator) schedule(candidate runtimes.Candidate) {
	serverID := candidate.Server.ID
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.stopped || !coordinator.current(candidate) {
		return
	}
	if previous := coordinator.timers[serverID]; previous != nil {
		previous.Stop()
	}
	now := coordinator.clock.Now()
	delay := NextPoll(now, PollOffset(coordinator.installationID, serverID)).Sub(now)
	var timer runtimes.Timer
	timer = coordinator.scheduler.AfterFunc(delay, func() {
		coordinator.mu.Lock()
		if coordinator.stopped || coordinator.timers[serverID] != timer {
			coordinator.mu.Unlock()
			return
		}
		delete(coordinator.timers, serverID)
		current, exists := coordinator.candidates[serverID]
		current.OperationID = nil
		coordinator.mu.Unlock()
		if exists {
			coordinator.run(coordinator.ctx, current, runtimes.CatalogTraversalPoll)
		}
	})
	coordinator.timers[serverID] = timer
}

func allowsHeaderBindings(candidate runtimes.Candidate, runtime *downstream.Runtime) bool {
	if runtime == nil || runtime.Era() != downstream.EraModern {
		return false
	}
	transport, err := servers.DecodeTransport(candidate.Server.Transport)
	if err != nil {
		return false
	}
	_, ok := transport.(contract.StreamableHTTPTransport)
	return ok
}

func catalogFailureReason(err error) contract.PublicReason {
	switch {
	case errors.Is(err, ErrTraversalLimit), errors.Is(err, servers.ErrResourceLimit):
		return contract.ReasonCatalogLimit
	case errors.Is(err, ErrInvalidPage), errors.Is(err, ErrCursorCycle), errors.Is(err, ErrNameCollision), errors.Is(err, servers.ErrInvalidInput):
		return contract.ReasonCatalogInvalid
	case errors.Is(err, servers.ErrStaleRevision):
		return contract.ReasonSuperseded
	default:
		return contract.ReasonConnectivity
	}
}

func postCommitFailure(failure *PublicationFailure) (contract.PublicReason, runtimes.CatalogPostCommitCause) {
	switch failure.Cause {
	case PublicationFailureStale:
		return contract.ReasonSuperseded, runtimes.CatalogPostCommitStale
	case PublicationFailureDrain:
		return contract.ReasonInterrupted, runtimes.CatalogPostCommitDrain
	default:
		return contract.ReasonConnectivity, runtimes.CatalogPostCommitStorage
	}
}

func catalogRuntimeFailure(err error, client PageClient) *runtimes.FailureDisposition {
	var requestFailure *requestFailure
	if !errors.As(err, &requestFailure) || errors.Is(requestFailure.err, context.Canceled) || errors.Is(requestFailure.err, context.DeadlineExceeded) {
		return nil
	}
	var challenge *downstream.OAuthChallengeDisposition
	if errors.As(requestFailure.err, &challenge) {
		return nil
	}
	_, concreteRuntime := client.(*downstream.Runtime)
	knownFatal := errors.Is(requestFailure.err, downstream.ErrAuthenticationRejected) || errors.Is(requestFailure.err, downstream.ErrSessionLost) || errors.Is(requestFailure.err, downstream.ErrTransportClosed) || errors.Is(requestFailure.err, downstream.ErrRemoteUnavailable) || errors.Is(requestFailure.err, downstream.ErrInvalidMessage) || errors.Is(requestFailure.err, downstream.ErrResponseMismatch)
	if !concreteRuntime && !knownFatal {
		return nil
	}
	failure := runtimes.ClassifyFailure(requestFailure.err)
	if !failure.RuntimeLost {
		return nil
	}
	return &failure
}

func cloneOperationID(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func catalogOutcome(intent runtimes.CatalogTraversalIntent, state contract.ActiveCatalogState, reason contract.PublicReason) runtimes.CatalogOutcome {
	return runtimes.CatalogOutcome{State: state, Reason: &reason, Intent: intent, RuntimeHealth: runtimes.CatalogRuntimeHealthy}
}
