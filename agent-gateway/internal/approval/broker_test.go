package approval_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/approval"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
)

// waitForPending polls until the broker has at least n pending entries or the
// deadline is reached.
func waitForPending(b *approval.Broker, n int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.Pending()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func makeRequest(id string) proxy.ApprovalRequest {
	return proxy.ApprovalRequest{
		RequestID: id,
		Agent:     "test-agent",
		Host:      "api.example.com:443",
		Method:    "GET",
		Path:      "/v1/resource",
	}
}

// TestRequest_ResolvesOnApprove verifies that a Request call returns
// DecisionApproved when Decide is called with DecisionApproved.
func TestRequest_ResolvesOnApprove(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: 10 * time.Second})
	req := makeRequest("01J0000000000000000000001A")

	var (
		got proxy.ApprovalDecision
		err error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err = b.Request(context.Background(), req)
	}()

	waitForPending(b, 1)
	require.NoError(t, b.Decide(req.RequestID, proxy.DecisionApproved))

	<-done
	require.NoError(t, err)
	assert.Equal(t, proxy.DecisionApproved, got)

	// Entry must be removed after resolution.
	assert.Empty(t, b.Pending())
}

// TestRequest_DeniedReturnsDecision verifies that a Request call returns
// DecisionDenied when Decide is called with DecisionDenied.
func TestRequest_DeniedReturnsDecision(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: 10 * time.Second})
	req := makeRequest("01J0000000000000000000002B")

	var (
		got proxy.ApprovalDecision
		err error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err = b.Request(context.Background(), req)
	}()

	waitForPending(b, 1)
	require.NoError(t, b.Decide(req.RequestID, proxy.DecisionDenied))

	<-done
	require.NoError(t, err)
	assert.Equal(t, proxy.DecisionDenied, got)
	assert.Empty(t, b.Pending())
}

// TestRequest_TimeoutReturnsErrTimeout verifies that Request returns
// DecisionTimeout when the broker's Timeout elapses, and that the pending
// entry is removed after the timeout.
func TestRequest_TimeoutReturnsErrTimeout(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: 50 * time.Millisecond})
	req := makeRequest("01J0000000000000000000003C")

	decision, err := b.Request(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, proxy.DecisionTimeout, decision)

	// Pending entry must be removed after the timeout path.
	assert.Empty(t, b.Pending())
}

// TestRequest_ContextCancelRemovesPending verifies that when the caller's
// context is cancelled mid-wait, the pending entry is removed immediately
// (ApprovalGuard pattern from §13).
func TestRequest_ContextCancelRemovesPending(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: time.Hour})
	req := makeRequest("01J0000000000000000000004D")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var reqErr error
	go func() {
		defer close(done)
		_, reqErr = b.Request(ctx, req)
	}()

	waitForPending(b, 1)
	assert.Len(t, b.Pending(), 1, "entry should be present before cancel")

	cancel()
	<-done

	// The error must be the context cancellation error.
	require.Error(t, reqErr)
	assert.True(t, errors.Is(reqErr, context.Canceled))

	// Entry must be removed by the ApprovalGuard defer.
	assert.Empty(t, b.Pending())
}

// TestRequest_QueueFullReturnsErrQueueFull verifies that when the pending map
// is at MaxPending capacity, a new Request returns ErrQueueFull synchronously.
func TestRequest_QueueFullReturnsErrQueueFull(t *testing.T) {
	b := approval.New(approval.Opts{MaxPending: 1, Timeout: time.Hour})

	req1 := makeRequest("01J0000000000000000000005E")
	req2 := makeRequest("01J0000000000000000000006F")

	// Enqueue req1 — it will block waiting for a decision.
	go func() { _, _ = b.Request(context.Background(), req1) }()
	waitForPending(b, 1)

	// Now the queue is full; req2 must fail synchronously.
	_, err := b.Request(context.Background(), req2)
	assert.ErrorIs(t, err, approval.ErrQueueFull)

	// Clean up the blocked goroutine.
	_ = b.Decide(req1.RequestID, proxy.DecisionApproved)
}

// TestBroker_Pending_ViewInvariant verifies the §8 approval view invariant:
// Pending() returns exactly what the caller placed in ApprovalRequest at
// construction time. The broker does not add body contents or extra header
// values. A request constructed with only rule-asserted fields (no body, only
// asserted headers) surfaces unchanged.
func TestBroker_Pending_ViewInvariant(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: time.Hour})

	// Simulate a PendingRequest built by the proxy pipeline: only the fields
	// the matched rule asserted are present. There is no body, and headers
	// contain only what the rule matched on (here, a single asserted header).
	asserted := make(http.Header)
	asserted.Set("X-Custom-Assert", "expected-value")

	req := proxy.ApprovalRequest{
		RequestID: "01J0000000000000000000007G",
		Agent:     "codex",
		Host:      "api.example.com:443",
		Method:    "POST",
		Path:      "/v2/upload",
		// Header contains ONLY the asserted header — the caller (proxy pipeline)
		// is responsible for stripping non-asserted headers before constructing
		// the ApprovalRequest (§8 approval view invariant, enforced at
		// construction time).
		Header: asserted,
	}

	go func() { _, _ = b.Request(context.Background(), req) }()
	waitForPending(b, 1)

	pending := b.Pending()
	require.Len(t, pending, 1)

	view := pending[0]
	assert.Equal(t, req.RequestID, view.RequestID)
	assert.Equal(t, req.Agent, view.Agent)
	assert.Equal(t, req.Host, view.Host)
	assert.Equal(t, req.Method, view.Method)
	assert.Equal(t, req.Path, view.Path)

	// The header in the view must be exactly the asserted headers — no extras.
	assert.Equal(t, asserted, view.Header)

	// Clean up.
	_ = b.Decide(req.RequestID, proxy.DecisionApproved)
}

// TestDecide_UnknownIDReturnsError verifies that Decide returns ErrUnknownID
// for an id that has never been registered (or has already been resolved).
func TestDecide_UnknownIDReturnsError(t *testing.T) {
	b := approval.New(approval.Opts{})
	err := b.Decide("does-not-exist", proxy.DecisionApproved)
	assert.ErrorIs(t, err, approval.ErrUnknownID)
}

// TestDecide_IdempotentAfterResolution verifies that calling Decide a second
// time for the same id (after it has already been resolved) returns
// ErrUnknownID rather than blocking or panicking.
func TestDecide_IdempotentAfterResolution(t *testing.T) {
	b := approval.New(approval.Opts{Timeout: 10 * time.Second})
	req := makeRequest("01J0000000000000000000008H")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = b.Request(context.Background(), req)
	}()

	waitForPending(b, 1)
	require.NoError(t, b.Decide(req.RequestID, proxy.DecisionApproved))
	<-done

	// Second Decide on the same id must return ErrUnknownID.
	err := b.Decide(req.RequestID, proxy.DecisionApproved)
	assert.ErrorIs(t, err, approval.ErrUnknownID)
}
