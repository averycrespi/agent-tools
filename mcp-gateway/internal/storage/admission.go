package storage

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
)

var (
	ErrMutationWaitFull    = errors.New("invocation mutation wait capacity is exhausted")
	ErrMutationWaitExpired = errors.New("invocation mutation acquisition wait expired")
	ErrMutationWaitStopped = errors.New("invocation mutation waiting is stopped")
)

type mutationAdmission struct {
	mu      sync.Mutex
	owned   bool
	waiters []*mutationWaiter
}

type mutationWaiter struct {
	ctx      context.Context
	stop     <-chan struct{}
	deadline time.Time
	ready    chan struct{}
	granted  bool
	err      error
}

// MutationOccupancy reports actual ownership (including a reserved handoff) and
// retained invocation waiters. It never reads or writes audit storage.
func (store *Store) MutationOccupancy() (owned bool, waiting int) {
	store.mutations.mu.Lock()
	defer store.mutations.mu.Unlock()
	return store.mutations.owned, len(store.mutations.waiters)
}

func (store *Store) acquireMutation(ctx context.Context, stop <-chan struct{}, wait bool) error {
	admission := &store.mutations
	admission.mu.Lock()
	if wait {
		if err := store.invocationWaitError(ctx, stop); err != nil {
			admission.mu.Unlock()
			return err
		}
	}
	if !admission.owned {
		admission.owned = true
		admission.mu.Unlock()
		return nil
	}
	if !wait {
		admission.mu.Unlock()
		return ErrMutationBusy
	}
	if len(admission.waiters) == contract.InvocationMutationWaiters {
		admission.mu.Unlock()
		return ErrMutationWaitFull
	}
	waiter := &mutationWaiter{
		ctx: ctx, stop: stop, deadline: time.Now().Add(contract.InvocationMutationWaitDeadline), ready: make(chan struct{}),
	}
	admission.waiters = append(admission.waiters, waiter)
	admission.mu.Unlock()
	store.mutationEvent(ctx, diagnostics.StorageWait, diagnostics.None, diagnostics.NoStage, 0)
	timer := time.NewTimer(time.Until(waiter.deadline))
	defer timer.Stop()
	select {
	case <-waiter.ready:
	case <-ctx.Done():
	case <-stop:
	case <-timer.C:
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if waiter.granted {
		// The acquisition timer does not cancel an already transferred slot.
		// Cancellation, drain and latch are nevertheless rechecked by its owner.
		return nil
	}
	if waiter.err != nil {
		return waiter.err
	}
	for index, candidate := range admission.waiters {
		if candidate == waiter {
			copy(admission.waiters[index:], admission.waiters[index+1:])
			admission.waiters[len(admission.waiters)-1] = nil
			admission.waiters = admission.waiters[:len(admission.waiters)-1]
			break
		}
	}
	if err := store.invocationWaitError(ctx, stop); err != nil {
		return err
	}
	return ErrMutationWaitExpired
}

func (store *Store) releaseMutation() {
	admission := &store.mutations
	admission.mu.Lock()
	defer admission.mu.Unlock()
	for len(admission.waiters) != 0 {
		waiter := admission.waiters[0]
		admission.waiters[0] = nil
		admission.waiters = admission.waiters[1:]
		waiter.err = store.invocationWaitError(waiter.ctx, waiter.stop)
		if waiter.err == nil && !time.Now().Before(waiter.deadline) {
			waiter.err = ErrMutationWaitExpired
		}
		if waiter.err == nil {
			waiter.granted = true
		}
		close(waiter.ready)
		if waiter.granted {
			return // Reserve the same owner; no arrival can barge into this handoff.
		}
	}
	admission.owned = false
	admission.waiters = nil
}

func (store *Store) invocationWaitError(ctx context.Context, stop <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-stop:
		return ErrMutationWaitStopped
	default:
	}
	if store.Latched() {
		return ErrStorageLatched
	}
	return nil
}
