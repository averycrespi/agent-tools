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
}

type refreshWork struct {
	done   chan struct{}
	cancel context.CancelFunc
	result runtimes.CatalogOutcome
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
	ctx            context.Context
	cancel         context.CancelFunc

	mu         sync.Mutex
	work       map[string]*refreshWork
	timers     map[string]runtimes.Timer
	candidates map[string]runtimes.Candidate
	stopped    bool
}

func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.InstallationID == "" || options.Repository == nil || options.Active == nil || options.Traverser == nil || options.Clock == nil || options.Scheduler == nil || options.Client == nil || options.Current == nil {
		return nil, servers.ErrInvalidInput
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{installationID: options.InstallationID, repository: options.Repository, active: options.Active, traverser: options.Traverser, clock: options.Clock, scheduler: options.Scheduler, client: options.Client, current: options.Current, ctx: ctx, cancel: cancel, work: make(map[string]*refreshWork), timers: make(map[string]runtimes.Timer), candidates: make(map[string]runtimes.Candidate)}, nil
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
	return coordinator.Refresh(ctx, candidate)
}

func (coordinator *Coordinator) Refresh(ctx context.Context, candidate runtimes.Candidate) runtimes.CatalogOutcome {
	serverID := candidate.Server.ID
	coordinator.mu.Lock()
	if coordinator.stopped {
		coordinator.mu.Unlock()
		return unavailableCatalogOutcome(contract.ReasonInterrupted)
	}
	if current := coordinator.work[serverID]; current != nil {
		done := current.done
		coordinator.mu.Unlock()
		select {
		case <-done:
			return current.result
		case <-ctx.Done():
			return unavailableCatalogOutcome(contract.ReasonCancelled)
		}
	}
	workCtx, cancel := context.WithCancel(coordinator.ctx)
	current := &refreshWork{done: make(chan struct{}), cancel: cancel}
	coordinator.work[serverID] = current
	coordinator.candidates[serverID] = candidate
	coordinator.mu.Unlock()

	result := coordinator.execute(workCtx, candidate)
	cancel()
	coordinator.mu.Lock()
	current.result = result
	delete(coordinator.work, serverID)
	close(current.done)
	stopped := coordinator.stopped
	coordinator.mu.Unlock()
	if !stopped && coordinator.live(candidate) {
		coordinator.schedule(candidate)
	}
	return result
}

func (coordinator *Coordinator) Withdraw(candidate runtimes.Candidate, state contract.ActiveCatalogState) {
	coordinator.mu.Lock()
	if timer := coordinator.timers[candidate.Server.ID]; timer != nil {
		timer.Stop()
		delete(coordinator.timers, candidate.Server.ID)
	}
	if work := coordinator.work[candidate.Server.ID]; work != nil {
		work.cancel()
	}
	delete(coordinator.candidates, candidate.Server.ID)
	coordinator.mu.Unlock()
	coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, state)
}

func (coordinator *Coordinator) Shutdown() {
	coordinator.mu.Lock()
	if coordinator.stopped {
		coordinator.mu.Unlock()
		return
	}
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
	coordinator.mu.Unlock()
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

func (coordinator *Coordinator) execute(ctx context.Context, candidate runtimes.Candidate) runtimes.CatalogOutcome {
	if !coordinator.live(candidate) {
		coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
		return unavailableCatalogOutcome(contract.ReasonSuperseded)
	}
	client, ok := coordinator.client(candidate)
	if !ok {
		return coordinator.failure(candidate, contract.ReasonProtocolUnsupported)
	}
	raw, err := coordinator.traverser.Traverse(ctx, client, candidate.Server.Namespace)
	if err != nil {
		return coordinator.failure(candidate, catalogFailureReason(err))
	}
	runtime, _ := client.(*downstream.Runtime)
	normalized := NormalizeCandidate(raw, NormalizeOptions{ServerID: candidate.Server.ID, AllowHeaderBindings: allowsHeaderBindings(candidate, runtime)})
	durable, err := coordinator.repository.Status(ctx, candidate.Server.ID)
	if err != nil {
		return coordinator.failure(candidate, contract.ReasonConnectivity)
	}
	revision := "0"
	if durable.Revision != nil {
		revision = *durable.Revision
	}
	status, err := coordinator.active.Publish(ctx, Publication{
		Fence:     coordinator.commitFence(candidate, revision),
		RuntimeID: candidate.RuntimeID, RuntimeGeneration: candidate.Generation, Candidate: normalized, Runtime: runtime,
		Current: func() bool { return coordinator.live(candidate) },
	})
	if err != nil {
		return coordinator.failure(candidate, catalogFailureReason(err))
	}
	return runtimes.CatalogOutcome{State: status.State}
}

func (coordinator *Coordinator) failure(candidate runtimes.Candidate, reason contract.PublicReason) runtimes.CatalogOutcome {
	live := coordinator.live(candidate)
	if live {
		active := coordinator.active.Status(candidate.Server.ID)
		durable, err := coordinator.repository.Status(coordinator.ctx, candidate.Server.ID)
		revision := "0"
		if err == nil && durable.Revision != nil {
			revision = *durable.Revision
		}
		if active.State == contract.ActiveCatalogCurrent || active.State == contract.ActiveCatalogStale {
			const issues = int64(1)
			if durable.State == contract.DurableCatalogStale && durable.IssueCount == issues {
				err = nil
			} else {
				_, err = coordinator.repository.SetState(coordinator.ctx, coordinator.commitFence(candidate, revision), contract.DurableCatalogStale, issues)
			}
			if err == nil {
				coordinator.active.MarkStaleExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, issues)
				return runtimes.CatalogOutcome{State: contract.ActiveCatalogStale, Reason: &reason}
			}
		} else if err == nil {
			_, _ = coordinator.repository.SetState(coordinator.ctx, coordinator.commitFence(candidate, revision), contract.DurableCatalogUnavailable, 1)
		}
	}
	if live {
		coordinator.active.MarkUnavailableExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, 1)
	} else {
		coordinator.active.WithdrawExact(candidate.Server.ID, candidate.RuntimeID, candidate.Generation, contract.ActiveCatalogUnavailable)
	}
	return runtimes.CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &reason}
}

func (coordinator *Coordinator) commitFence(candidate runtimes.Candidate, revision string) CommitFence {
	return CommitFence{ServerID: candidate.Server.ID, ExpectedDesiredRevision: candidate.Server.DesiredRevision, ExpectedRegistrationRevision: candidate.Authority.RegistrationRevision, ExpectedCredentialRevisions: candidate.Authority.CredentialRevisions, ExpectedCatalogRevision: revision}
}

func (coordinator *Coordinator) live(candidate runtimes.Candidate) bool {
	coordinator.mu.Lock()
	current, exists := coordinator.candidates[candidate.Server.ID]
	live := !coordinator.stopped && exists && current.RuntimeID == candidate.RuntimeID && current.Generation == candidate.Generation
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
		current := coordinator.candidates[serverID]
		coordinator.mu.Unlock()
		go coordinator.Refresh(coordinator.ctx, current)
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

func unavailableCatalogOutcome(reason contract.PublicReason) runtimes.CatalogOutcome {
	return runtimes.CatalogOutcome{State: contract.ActiveCatalogUnavailable, Reason: &reason}
}
