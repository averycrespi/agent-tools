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
	// requests across all agents. When the cap is hit, new Request calls
	// return ErrQueueFull synchronously without blocking. Zero means no cap.
	MaxPending int

	// MaxPendingPerAgent is the maximum number of concurrently-pending
	// approval requests attributed to a single agent (keyed by Agent name).
	// When the cap is hit, new Request calls from that agent return
	// ErrQueueFull synchronously without blocking; other agents are unaffected.
	// Zero means no per-agent cap — only MaxPending applies.
	//
	// WHY: the per-agent cap prevents one runaway agent (e.g. a wedged retry
	// loop) from filling the global pending queue and starving parallel
	// agents that share the same gateway. Without it, MaxPending alone is a
	// noisy-neighbour footgun — one buggy sandbox DoS's every other sandbox.
	MaxPendingPerAgent int

	// Timeout is how long Request will block waiting for a decision before
	// returning proxy.DecisionTimeout. Zero means block forever.
	Timeout time.Duration

	// OnEvent, if non-nil, is called with a short kind string ("approval") and
	// a JSON-serialisable payload whenever a pending entry is added (on Request)
	// or resolved (on Decide). The callback must be non-blocking; it is called
	// while the broker mutex is NOT held.
	OnEvent func(kind string, data any)
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

	// agent is the Agent name captured at Request time, used to decrement the
	// per-agent counter on removal. Stored separately from view because view
	// is public-shape and agent is a bookkeeping key — we never want view.Agent
	// drift to silently break the per-agent accounting.
	agent string
}

// Broker is the in-memory approval broker.
//
// Create with New; do not copy after first use.
type Broker struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
	// perAgent tracks the number of currently-pending requests keyed by Agent
	// name. Incremented in Request after the cap check succeeds; decremented
	// in removeLocked on ANY pending removal path (Decide success, timeout,
	// ctx cancel). Keyed by the SAME string stored in pendingEntry.agent so
	// increment/decrement always pair. A missed decrement is a slow-leak bug
	// that eventually wedges the agent at its cap — audit every removal site.
	perAgent map[string]int
	opts     Opts
	onEvent  func(kind string, data any) // alias to opts.OnEvent; nil-safe
}

// New constructs a Broker with the given options.
func New(opts Opts) *Broker {
	return &Broker{
		pending:  make(map[string]*pendingEntry),
		perAgent: make(map[string]int),
		opts:     opts,
		onEvent:  opts.OnEvent,
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
	// Check queue caps before registering. Both caps are checked under the
	// same lock so a concurrent Request/Decide cannot race the counter.
	b.mu.Lock()
	if b.opts.MaxPending > 0 && len(b.pending) >= b.opts.MaxPending {
		b.mu.Unlock()
		return "", ErrQueueFull
	}
	// Per-agent cap: one runaway agent must not starve parallel agents that
	// share the same gateway. Evaluated independently of the global cap —
	// an agent that hits its per-agent cap is rejected even when the global
	// queue has room.
	if b.opts.MaxPendingPerAgent > 0 && b.perAgent[p.Agent] >= b.opts.MaxPendingPerAgent {
		b.mu.Unlock()
		return "", ErrQueueFull
	}

	entry := &pendingEntry{
		view:  p,
		ch:    make(chan proxy.ApprovalDecision, 1),
		agent: p.Agent,
	}
	b.pending[p.RequestID] = entry
	b.perAgent[p.Agent]++
	b.mu.Unlock()

	// Notify listeners (e.g. dashboard SSE) that a new pending item was added.
	if b.onEvent != nil {
		b.onEvent("approval", p)
	}

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
	// not include it after the decision has been dispatched. removeEntryLocked
	// also decrements the per-agent counter — Decide is one of the three paths
	// that resolves a pending request (alongside timeout and ctx cancel), and
	// skipping the decrement here would permanently leak a slot.
	b.removeEntryLocked(id, entry)
	b.mu.Unlock()

	// The channel is buffered(1); this never blocks.
	entry.ch <- decision

	// Notify listeners that the pending item was resolved.
	if b.onEvent != nil {
		b.onEvent("approval", struct {
			RequestID string                 `json:"request_id"`
			Decision  proxy.ApprovalDecision `json:"decision"`
		}{RequestID: id, Decision: decision})
	}
	return nil
}

// remove deletes the entry for id from the pending map and decrements the
// per-agent counter. It is a no-op if id is not present (idempotent).
//
// All pending-removal paths (Decide, timeout select arm, ApprovalGuard defer
// on ctx cancel) MUST funnel through remove or removeEntryLocked — missing
// a decrement leaks a per-agent slot permanently and eventually wedges that
// agent at its cap.
func (b *Broker) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pending[id]
	if !ok {
		return
	}
	b.removeEntryLocked(id, entry)
}

// removeEntryLocked deletes entry from pending and decrements the per-agent
// counter. Caller must hold b.mu. The counter is deleted from the map when
// it hits zero to keep the map bounded (idle agents do not accumulate keys).
func (b *Broker) removeEntryLocked(id string, entry *pendingEntry) {
	delete(b.pending, id)
	if n := b.perAgent[entry.agent] - 1; n <= 0 {
		delete(b.perAgent, entry.agent)
	} else {
		b.perAgent[entry.agent] = n
	}
}

// Ensure *Broker satisfies proxy.ApprovalBroker at compile time.
var _ proxy.ApprovalBroker = (*Broker)(nil)
