package authorization

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type leasePhase uint32

const (
	leasePending leasePhase = iota
	leaseAdmitted
	leaseCancelled
	leaseReleased
)

type Lease struct {
	owner   *authorityRegistry
	binding CredentialBinding
	done    chan struct{}
	phase   atomic.Uint32
}

func (lease *Lease) Binding() CredentialBinding {
	if lease == nil {
		return CredentialBinding{}
	}
	return lease.binding
}

func (lease *Lease) Done() <-chan struct{} {
	if lease == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return lease.done
}

func (lease *Lease) Current() bool {
	return lease != nil && leasePhase(lease.phase.Load()) == leasePending &&
		!lease.owner.draining.Load() && !lease.owner.store.Latched()
}

func (lease *Lease) Release() {
	if lease == nil {
		return
	}
	for {
		phase := leasePhase(lease.phase.Load())
		if phase == leaseCancelled || phase == leaseReleased {
			lease.owner.remove(lease)
			return
		}
		if lease.phase.CompareAndSwap(uint32(phase), uint32(leaseReleased)) {
			close(lease.done)
			lease.owner.remove(lease)
			return
		}
	}
}

func (lease *Lease) cancel() {
	if lease.phase.CompareAndSwap(uint32(leasePending), uint32(leaseCancelled)) {
		close(lease.done)
	}
}

type authorityHooks struct {
	afterGateAcquire   func()
	afterBindingRead   func()
	afterLeaseRegister func()
	afterDrainFence    func()
}

type authorityRegistry struct {
	diagnostics   diagnostics.AuthorityObserver
	diagnosticIDs atomic.Uint64
	store         *storage.Store
	gate          chan struct{}
	draining      atomic.Bool

	mu       sync.Mutex
	work     int64
	idle     chan struct{}
	stopping chan struct{}
	leases   map[*Lease]struct{}
	hooks    authorityHooks
}

func newAuthorityRegistry(store *storage.Store) *authorityRegistry {
	idle := make(chan struct{})
	close(idle)
	return &authorityRegistry{
		store: store, gate: make(chan struct{}, 1), idle: idle,
		stopping: make(chan struct{}), leases: make(map[*Lease]struct{}),
	}
}

// SetDiagnostics binds the startup-owned authority observer before concurrent use.
func (repository *Repository) SetDiagnostics(observer diagnostics.AuthorityObserver) {
	repository.authority.diagnostics = observer
}

func (registry *authorityRegistry) authorityEvent(ctx context.Context, event diagnostics.Event, cause diagnostics.Cause, id uint64, duration time.Duration) {
	if registry.diagnostics == nil || !registry.diagnostics.DebugEnabled() {
		return
	}
	owned, waiting := registry.authorityOccupancy()
	registry.diagnostics.Authority(diagnostics.Facts{Event: event, Cause: cause, Call: diagnostics.FromContext(ctx).Call, Mutation: id, Duration: duration, Owned: owned, Waiting: waiting, Limit: 32})
}

// The channel remains the actual exclusive owner, not a duplicate diagnostic
// counter. While mu freezes work, its length is the snapshot's linearization
// point. Gate retirement and work retirement hold mu together.
func (registry *authorityRegistry) authorityOccupancy() (owned, waiting int) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	owned = len(registry.gate)
	return owned, int(registry.work) - owned
}

func (registry *authorityRegistry) acquire(ctx context.Context) (released func(), result error) {
	var started time.Time
	var id uint64
	cause := diagnostics.Capacity
	if registry.diagnostics != nil && registry.diagnostics.DebugEnabled() {
		started = time.Now()
		id = diagnostics.NextID(&registry.diagnosticIDs)
		defer func() {
			if result != nil {
				switch {
				case errors.Is(result, context.Canceled), errors.Is(result, context.DeadlineExceeded):
					cause = diagnostics.Cancelled
				case errors.Is(result, ErrShuttingDown):
					cause = diagnostics.Stopped
				case errors.Is(result, ErrStorageUnavailable):
					cause = diagnostics.Latched
				}
				registry.authorityEvent(ctx, diagnostics.AuthorityReject, cause, id, time.Since(started))
			} else {
				acquired := time.Now()
				registry.authorityEvent(ctx, diagnostics.AuthorityAcquire, diagnostics.Success, id, acquired.Sub(started))
				original := released
				released = func() {
					original()
					registry.authorityEvent(ctx, diagnostics.AuthorityRelease, diagnostics.Success, id, time.Since(acquired))
				}
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := registry.reserveWork(); err != nil {
		return nil, err
	}
	if !started.IsZero() {
		owned, _ := registry.authorityOccupancy()
		if owned > 0 {
			registry.authorityEvent(ctx, diagnostics.AuthorityWait, diagnostics.None, id, 0)
		}
	}
	wait, cancel := context.WithTimeout(ctx, contract.AuthorityWaitDeadline)
	defer cancel()
	release := registry.releaseWork
	select {
	case registry.gate <- struct{}{}:
		release = registry.releaseGate
	case <-wait.Done():
	case <-registry.stopping:
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if registry.draining.Load() {
		release()
		return nil, ErrShuttingDown
	}
	if wait.Err() != nil {
		cause = diagnostics.Expired
		release()
		return nil, ErrResourceLimit
	}
	if registry.hooks.afterGateAcquire != nil {
		registry.hooks.afterGateAcquire()
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if registry.draining.Load() {
		release()
		return nil, ErrShuttingDown
	}
	return release, nil
}

func (registry *authorityRegistry) reserveWork() error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() {
		return ErrShuttingDown
	}
	if registry.work >= mustLimit("authority_work") {
		return ErrResourceLimit
	}
	if registry.work == 0 {
		registry.idle = make(chan struct{})
	}
	registry.work++
	return nil
}

func (registry *authorityRegistry) releaseGate() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	// Only the acquired owner receives this already-present token; this cannot
	// wait for application work and permits the next sender's bounded handoff.
	<-registry.gate
	registry.releaseWorkLocked()
}

func (registry *authorityRegistry) releaseWork() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.releaseWorkLocked()
}

func (registry *authorityRegistry) releaseWorkLocked() {
	registry.work--
	if registry.work == 0 {
		close(registry.idle)
	}
}

func (registry *authorityRegistry) register(binding CredentialBinding) (*Lease, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.draining.Load() || registry.store.Latched() {
		if registry.store.Latched() {
			return nil, ErrStorageUnavailable
		}
		return nil, ErrShuttingDown
	}
	lease := &Lease{owner: registry, binding: binding, done: make(chan struct{})}
	lease.phase.Store(uint32(leasePending))
	registry.leases[lease] = struct{}{}
	return lease, nil
}

func (registry *authorityRegistry) remove(lease *Lease) {
	registry.mu.Lock()
	delete(registry.leases, lease)
	registry.mu.Unlock()
}

func (registry *authorityRegistry) cancelPending() {
	registry.cancelMatching(func(*Lease) bool { return true })
}

func (registry *authorityRegistry) cancelPrincipal(principalID string) {
	registry.cancelMatching(func(lease *Lease) bool { return lease.binding.PrincipalID == principalID })
}

func (registry *authorityRegistry) cancelMatching(matches func(*Lease) bool) {
	registry.mu.Lock()
	leases := make([]*Lease, 0, len(registry.leases))
	for lease := range registry.leases {
		if matches(lease) {
			delete(registry.leases, lease)
			leases = append(leases, lease)
		}
	}
	registry.mu.Unlock()
	for _, lease := range leases {
		lease.cancel()
	}
}

func (repository *Repository) mutateAuthorityTx(ctx context.Context, affectedPrincipalID string, mutate func(*sql.Tx) error) error {
	return repository.mutateAuthority(ctx, affectedPrincipalID, func() error {
		return repository.store.Mutate(ctx, mutate)
	})
}

func (repository *Repository) mutateCredentialCandidate(
	ctx context.Context,
	affectedPrincipalID string,
	candidate storage.AgentCredentialCandidate,
	mutate func(*sql.Tx) error,
) error {
	return repository.mutateAuthority(ctx, affectedPrincipalID, func() error {
		return repository.store.MutateAgentCredentialCandidate(ctx, candidate, mutate)
	})
}

func (repository *Repository) mutateAuthority(ctx context.Context, affectedPrincipalID string, mutate func() error) error {
	releaseGate, err := repository.authority.acquire(ctx)
	if err != nil {
		return err
	}
	defer releaseGate()
	err = mutate()
	if affectedPrincipalID != "" && (err == nil || repository.store.Latched()) {
		repository.authority.cancelPrincipal(affectedPrincipalID)
	}
	return err
}

func (repository *Repository) BeginDrain() {
	registry := repository.authority
	registry.mu.Lock()
	if !registry.draining.CompareAndSwap(false, true) {
		registry.mu.Unlock()
		return
	}
	close(registry.stopping)
	registry.mu.Unlock()
	if registry.hooks.afterDrainFence != nil {
		registry.hooks.afterDrainFence()
	}
	registry.cancelPending()
}

func (repository *Repository) Drain(ctx context.Context) error {
	repository.BeginDrain()
	registry := repository.authority
	if err := ctx.Err(); err != nil {
		return err
	}
	registry.mu.Lock()
	idle := registry.idle
	registry.mu.Unlock()
	select {
	case <-idle:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
