package storage

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitMutationOccupancy(t *testing.T, store *Store, owned bool, waiting int) {
	t.Helper()
	require.Eventually(t, func() bool {
		active, queued := store.MutationOccupancy()
		return active == owned && queued == waiting
	}, time.Second, time.Millisecond)
}

func TestInvocationMutationFIFOAndBoundedCapacity(t *testing.T) {
	store := &Store{}
	ctx := t.Context()
	require.NoError(t, store.acquireMutation(ctx, nil, false))
	results := make([]chan error, contract.InvocationMutationWaiters)
	for index := range results {
		results[index] = make(chan error, 1)
		go func() { results[index] <- store.acquireMutation(ctx, nil, true) }()
		waitMutationOccupancy(t, store, true, index+1)
	}
	start := time.Now()
	require.ErrorIs(t, store.acquireMutation(ctx, nil, true), ErrMutationWaitFull)
	require.Less(t, time.Since(start), contract.InvocationMutationWaitDeadline)
	require.ErrorIs(t, store.acquireMutation(ctx, nil, false), ErrMutationBusy)
	for index := range results {
		store.releaseMutation()
		select {
		case err := <-results[index]:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("FIFO waiter did not acquire")
		}
		waitMutationOccupancy(t, store, true, len(results)-index-1)
		require.ErrorIs(t, store.acquireMutation(ctx, nil, false), ErrMutationBusy, "reserved owner prevents foreign barging")
		for later := index + 1; later < len(results); later++ {
			select {
			case <-results[later]:
				t.Fatal("later waiter bypassed owner")
			default:
			}
		}
	}
	store.releaseMutation()
	waitMutationOccupancy(t, store, false, 0)
}

func TestInvocationMutationHeadCancellationAndWaitExpiry(t *testing.T) {
	for _, cause := range []string{"cancel", "deadline", "drain", "expiry", "latch"} {
		t.Run(cause, func(t *testing.T) {
			store := &Store{}
			require.NoError(t, store.acquireMutation(t.Context(), nil, false))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cause == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer deadlineCancel()
			}
			stop := make(chan struct{})
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- store.acquireMutation(ctx, stop, true) }()
			waitMutationOccupancy(t, store, true, 1)
			want := ErrMutationWaitExpired
			switch cause {
			case "cancel":
				cancel()
				want = context.Canceled
			case "deadline":
				want = context.DeadlineExceeded
			case "drain":
				close(stop)
				want = ErrMutationWaitStopped
			case "latch":
				store.latched.Store(true)
				store.releaseMutation()
				want = ErrStorageLatched
			}
			require.ErrorIs(t, <-done, want)
			if cause == "expiry" {
				require.GreaterOrEqual(t, time.Since(start), contract.InvocationMutationWaitDeadline)
				require.Less(t, time.Since(start), time.Second)
			}
			if cause != "latch" {
				waitMutationOccupancy(t, store, true, 0)
				require.False(t, store.Latched())
				next := make(chan error, 1)
				go func() { next <- store.acquireMutation(t.Context(), nil, true) }()
				waitMutationOccupancy(t, store, true, 1)
				store.releaseMutation()
				require.NoError(t, <-next)
				store.releaseMutation()
			}
			waitMutationOccupancy(t, store, false, 0)
		})
	}
}

func TestInvocationMutationRemovesCancelledHeadWithoutLosingTail(t *testing.T) {
	store := &Store{}
	require.NoError(t, store.acquireMutation(t.Context(), nil, false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	head, tail := make(chan error, 1), make(chan error, 1)
	go func() { head <- store.acquireMutation(ctx, nil, true) }()
	waitMutationOccupancy(t, store, true, 1)
	go func() { tail <- store.acquireMutation(t.Context(), nil, true) }()
	waitMutationOccupancy(t, store, true, 2)
	cancel()
	require.ErrorIs(t, <-head, context.Canceled)
	waitMutationOccupancy(t, store, true, 1)
	store.releaseMutation()
	require.NoError(t, <-tail)
	store.releaseMutation()
	waitMutationOccupancy(t, store, false, 0)
}

func TestInvocationMutationExpiredHandoffNeverGrantsOwnership(t *testing.T) {
	store := &Store{}
	require.NoError(t, store.acquireMutation(t.Context(), nil, false))
	// Force expiry at the handoff boundary without waiting for timer delivery.
	waiter := &mutationWaiter{ctx: t.Context(), deadline: time.Now().Add(-time.Second), ready: make(chan struct{})}
	store.mutations.waiters = append(store.mutations.waiters, waiter)
	store.releaseMutation()
	<-waiter.ready
	require.False(t, waiter.granted)
	require.ErrorIs(t, waiter.err, ErrMutationWaitExpired)
	waitMutationOccupancy(t, store, false, 0)
}

func TestInvocationMutationCancellationHandoffRace(t *testing.T) {
	// A bounded algorithm-only race matrix, not a repeated persistence suite.
	for range 64 {
		store := &Store{}
		require.NoError(t, store.acquireMutation(t.Context(), nil, false))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			err := store.acquireMutation(ctx, nil, true)
			if err == nil {
				store.releaseMutation()
			}
			done <- err
		}()
		waitMutationOccupancy(t, store, true, 1)
		var race sync.WaitGroup
		race.Add(2)
		go func() { defer race.Done(); cancel() }()
		go func() { defer race.Done(); store.releaseMutation() }()
		race.Wait()
		err := <-done
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
		waitMutationOccupancy(t, store, false, 0)
	}
}

func TestInvocationMutationAcquisitionDeadlineDoesNotCancelWork(t *testing.T) {
	ownership := newOwnership(t)
	store, err := Initialize(t.Context(), ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	// Own the slot without creating intent, to distinguish queued rejection from mutation.
	require.NoError(t, store.acquireMutation(t.Context(), nil, false))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- store.MutateInvocation(t.Context(), nil, func(tx *sql.Tx) error {
			close(entered)
			<-release
			_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision = revision + 1`)
			return err
		})
	}()
	waitMutationOccupancy(t, store, true, 1)
	_, err = os.Lstat(ownership.Layout().MutationMarker)
	require.ErrorIs(t, err, os.ErrNotExist)
	store.releaseMutation()
	<-entered
	// Intentionally outlive the compiled acquisition bound while SQL owns the slot.
	<-time.After(contract.InvocationMutationWaitDeadline + 20*time.Millisecond)
	require.ErrorIs(t, store.Mutate(t.Context(), func(*sql.Tx) error { t.Error("foreign callback ran"); return nil }), ErrMutationBusy)
	unblock()
	require.NoError(t, <-done)
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, identity.Revision)
	require.False(t, store.Latched())
	waitMutationOccupancy(t, store, false, 0)
}

func TestInvocationMutationPreWorkRejectionsHaveNoIntent(t *testing.T) {
	ownership := newOwnership(t)
	store, err := Initialize(t.Context(), ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var callbacks atomic.Int32
	callback := func(*sql.Tx) error { callbacks.Add(1); return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, store.MutateInvocation(ctx, nil, callback), context.Canceled)
	stop := make(chan struct{})
	close(stop)
	require.ErrorIs(t, store.MutateInvocation(t.Context(), stop, callback), ErrMutationWaitStopped)
	require.NoError(t, store.acquireMutation(t.Context(), nil, false))
	require.ErrorIs(t, store.MutateInvocation(t.Context(), nil, callback), ErrMutationWaitExpired)
	require.ErrorIs(t, store.MutateAgentCredentialCandidate(t.Context(), recoveryCandidate(), callback), ErrMutationBusy)
	store.releaseMutation()
	assert.Zero(t, callbacks.Load())
	assert.False(t, store.Latched())
	_, err = os.Lstat(ownership.Layout().MutationMarker)
	require.ErrorIs(t, err, os.ErrNotExist)
	// The invocation fence must not globally fence producer cleanup.
	require.NoError(t, store.Mutate(t.Context(), callback))
	require.EqualValues(t, 1, callbacks.Load())
	waitMutationOccupancy(t, store, false, 0)
}
