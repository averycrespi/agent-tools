// Package approval implements the in-memory approval broker for agent-gateway.
// It holds pending require-approval requests and allows an operator to resolve
// them via Decide; the requesting goroutine blocks until a decision arrives,
// the context is cancelled, or the configured timeout elapses.
//
// Concurrency: all exported methods are safe to call concurrently.
package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
)

// ErrQueueFull is returned by Request when the number of pending approvals
// has reached the configured MaxPending cap.
var ErrQueueFull = errors.New("approval: queue full")

// ErrUnknownID is returned by Decide when the given id does not correspond to
// any pending request (already resolved, timed out, or never created).
var ErrUnknownID = errors.New("approval: unknown request id")

// Opts configures the Broker.
type Opts struct {
	// MaxPending is the maximum number of concurrently-pending approval
	// requests. When the cap is hit, new Request calls return ErrQueueFull
	// synchronously without blocking. Zero means no cap.
	MaxPending int

	// Timeout is how long Request will block waiting for a decision before
	// returning proxy.DecisionTimeout. Zero means block forever.
	Timeout time.Duration
}

// pendingEntry is a single in-flight approval request stored in the broker.
// It is created in Request and removed on resolution (approve/deny), timeout,
// or context cancellation (ApprovalGuard cleanup).
type pendingEntry struct {
	// view is a sanitised snapshot of the request stored for Pending(). It
	// contains only the fields the rule asserted — body and non-asserted header
	// values are never stored here (§8 approval view invariant, enforced at
	// construction time by the caller, not the broker).
	view proxy.ApprovalRequest

	// ch receives the operator decision exactly once; buffered(1) so Decide
	// never blocks.
	ch chan proxy.ApprovalDecision
}

// Broker is the in-memory approval broker.
//
// Create with New; do not copy after first use.
type Broker struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
	opts    Opts
}

// New constructs a Broker with the given options.
func New(opts Opts) *Broker {
	return &Broker{
		pending: make(map[string]*pendingEntry),
		opts:    opts,
	}
}

// Request implements proxy.ApprovalBroker. It registers p as a pending entry
// and blocks until an operator decision arrives, ctx is cancelled, or the
// configured timeout elapses.
//
// The ApprovalGuard pattern (§13) ensures the pending entry is always removed:
//   - Normal resolution (approved/denied/timeout): disarm=true, defer is a no-op.
//   - Context cancellation: disarm=false, defer removes the entry.
func (b *Broker) Request(ctx context.Context, p proxy.ApprovalRequest) (proxy.ApprovalDecision, error) {
	// Check queue cap before registering.
	b.mu.Lock()
	if b.opts.MaxPending > 0 && len(b.pending) >= b.opts.MaxPending {
		b.mu.Unlock()
		return "", ErrQueueFull
	}

	entry := &pendingEntry{
		view: p,
		ch:   make(chan proxy.ApprovalDecision, 1),
	}
	b.pending[p.RequestID] = entry
	b.mu.Unlock()

	// ApprovalGuard: defer cleanup that fires unless disarmed by a normal-path
	// resolution. This ensures the entry is removed on context cancellation even
	// if Decide is never called.
	disarm := false
	defer func() {
		if !disarm {
			b.remove(p.RequestID)
		}
	}()

	// Build the timeout arm. If Timeout is zero we use a channel that never
	// fires (select will only wake on ctx.Done or ch).
	var timeoutC <-chan time.Time
	var timer *time.Timer
	if b.opts.Timeout > 0 {
		timer = time.NewTimer(b.opts.Timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	select {
	case decision := <-entry.ch:
		disarm = true
		return decision, nil

	case <-ctx.Done():
		// Agent disconnected mid-wait; guard's defer will clean up the entry.
		return "", ctx.Err()

	case <-timeoutC:
		disarm = true
		b.remove(p.RequestID)
		return proxy.DecisionTimeout, nil
	}
}

// Pending returns a snapshot of all currently-pending approval requests. The
// returned slice is safe to iterate concurrently with other Broker operations.
//
// The view in each entry was supplied by the caller at Request time and already
// contains only the fields the matched rule asserted (§8 approval view
// invariant). The broker does not perform additional filtering.
func (b *Broker) Pending() []proxy.ApprovalRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]proxy.ApprovalRequest, 0, len(b.pending))
	for _, e := range b.pending {
		out = append(out, e.view)
	}
	return out
}

// Decide resolves the pending request identified by id with the given decision.
// It returns ErrUnknownID if id is not in the pending map (already resolved,
// timed out, or cancelled).
//
// Decide is idempotent in the sense that calling it for an already-resolved id
// returns ErrUnknownID rather than panicking or blocking.
func (b *Broker) Decide(id string, decision proxy.ApprovalDecision) error {
	b.mu.Lock()
	entry, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return ErrUnknownID
	}
	// Remove the entry before sending so that a concurrent Pending() call does
	// not include it after the decision has been dispatched.
	delete(b.pending, id)
	b.mu.Unlock()

	// The channel is buffered(1); this never blocks.
	entry.ch <- decision
	return nil
}

// remove deletes the entry for id from the pending map. It is a no-op if id
// is not present (idempotent).
func (b *Broker) remove(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// Ensure *Broker satisfies proxy.ApprovalBroker at compile time.
var _ proxy.ApprovalBroker = (*Broker)(nil)
