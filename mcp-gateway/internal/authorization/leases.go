package authorization

import (
	"context"
	"sync"
	"sync/atomic"

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

	mu     sync.Mutex
	leases map[*Lease]struct{}
	hooks  authorityHooks
}

func newAuthorityRegistry(store *storage.Store) *authorityRegistry {
	return &authorityRegistry{store: store, gate: make(chan struct{}, 1), leases: make(map[*Lease]struct{})}
}

func (registry *authorityRegistry) tryAcquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry.draining.Load() {
		return nil, ErrShuttingDown
	}
	select {
	case registry.gate <- struct{}{}:
		if registry.hooks.afterGateAcquire != nil {
			registry.hooks.afterGateAcquire()
		}
		if err := ctx.Err(); err != nil {
			<-registry.gate
			return nil, err
		}
		if registry.draining.Load() {
			<-registry.gate
			return nil, ErrShuttingDown
		}
		return func() { <-registry.gate }, nil
	default:
		return nil, ErrResourceLimit
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
	registry.mu.Lock()
	leases := make([]*Lease, 0, len(registry.leases))
	for lease := range registry.leases {
		delete(registry.leases, lease)
		leases = append(leases, lease)
	}
	registry.mu.Unlock()
	for _, lease := range leases {
		lease.cancel()
	}
}

func (repository *Repository) Drain(ctx context.Context) error {
	registry := repository.authority
	registry.draining.Store(true)
	if registry.hooks.afterDrainFence != nil {
		registry.hooks.afterDrainFence()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case registry.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-registry.gate
			return err
		}
		registry.cancelPending()
		<-registry.gate
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
