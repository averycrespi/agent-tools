package authorization

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"

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
	store    *storage.Store
	gate     chan struct{}
	draining atomic.Bool

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

func (registry *authorityRegistry) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := registry.reserveWork(); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, contract.AuthorityWaitDeadline)
	defer cancel()
	release := registry.releaseWork
	select {
	case registry.gate <- struct{}{}:
		release = func() {
			<-registry.gate
			registry.releaseWork()
		}
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

func (registry *authorityRegistry) releaseWork() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
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
