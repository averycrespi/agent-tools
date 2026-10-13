package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type catalogAuditFixture struct {
	coordinator *Coordinator
	client      *coordinatorClient
	scheduler   *catalogScheduler
	candidate   runtimes.Candidate
}

func newCatalogAuditFixture(t *testing.T) catalogAuditFixture {
	t.Helper()
	repository, servers, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, servers, "audit")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	client := &coordinatorClient{result: json.RawMessage(`{"tools":[{"name":"one","inputSchema":{"type":"object"}}]}`)}
	scheduler := newCatalogScheduler()
	coordinator, err := NewCoordinator(CoordinatorOptions{InstallationID: catalogInstallationID, Repository: repository, Active: registry, Traverser: NewTraverser(), Clock: clock, Scheduler: scheduler, Client: func(runtimes.Candidate) (PageClient, bool) { return client, true }, Current: func(runtimes.Candidate) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.True(t, coordinator.Wait(ctx))
	})
	return catalogAuditFixture{coordinator, client, scheduler, coordinatorCandidate(t, servers, server)}
}

func holdCatalogAuditWriter(t *testing.T, store *storage.Store) func() {
	t.Helper()
	entered, released := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		defer cancel()
		done <- store.Mutate(ctx, func(*sql.Tx) error {
			close(entered)
			<-released
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("writer was not admitted: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("writer admission timed out")
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(released)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("writer release timed out")
			}
		})
	}
	t.Cleanup(release)
	return release
}

func waitCatalogAuditTimer(t *testing.T, scheduler *catalogScheduler, index int, delay time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool { return scheduler.count() > index }, 5*time.Second, time.Millisecond)
	require.Equal(t, delay, scheduler.call(index).delay)
}

func exhaustCatalogAuditWaits(t *testing.T, scheduler *catalogScheduler) {
	t.Helper()
	for index, delay := range []time.Duration{25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond} {
		waitCatalogAuditTimer(t, scheduler, index, delay)
		scheduler.fire(t, index)
	}
}

func receiveCatalogAuditResult(t *testing.T, results <-chan runtimes.CatalogOutcome) runtimes.CatalogOutcome {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("catalog owner did not settle")
		return runtimes.CatalogOutcome{}
	}
}

func TestCatalogAuditAdmissionTransientKeepsOneTraversal(t *testing.T) {
	for _, finish := range []bool{false, true} {
		name := "attempt"
		if finish {
			name = "outcome"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCatalogAuditFixture(t)
			started, peerRelease := make(chan struct{}), make(chan struct{})
			var releasePeer sync.Once
			unblockPeer := func() { releasePeer.Do(func() { close(peerRelease) }) }
			t.Cleanup(unblockPeer)
			var releaseWriter func()
			if finish {
				fixture.client.started, fixture.client.release = started, peerRelease
			} else {
				releaseWriter = holdCatalogAuditWriter(t, fixture.coordinator.repository.store)
			}
			results := make(chan runtimes.CatalogOutcome, 1)
			go func() { results <- fixture.coordinator.Activate(t.Context(), fixture.candidate) }()
			if finish {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("traversal did not start")
				}
				releaseWriter = holdCatalogAuditWriter(t, fixture.coordinator.repository.store)
				unblockPeer()
			}
			waitCatalogAuditTimer(t, fixture.scheduler, 0, 25*time.Millisecond)
			if finish {
				require.Equal(t, 1, fixture.client.callCount())
			} else {
				require.Zero(t, fixture.client.callCount())
			}
			waiterCtx, cancelWaiter := context.WithCancel(t.Context())
			cancelWaiter()
			joined := fixture.coordinator.Refresh(waiterCtx, fixture.candidate)
			require.Equal(t, contract.ReasonCancelled, *joined.Reason)
			releaseWriter()
			fixture.scheduler.fire(t, 0)
			outcome := receiveCatalogAuditResult(t, results)
			require.Equal(t, contract.ActiveCatalogCurrent, outcome.State)
			require.Equal(t, 1, fixture.client.callCount())
			require.Equal(t, 2, fixture.scheduler.count(), "one admission wait and one normal grid poll")
			reader, err := audit.NewRepository(fixture.coordinator.repository.store)
			require.NoError(t, err)
			page, err := reader.List(t.Context(), audit.Query{Limit: 100})
			require.NoError(t, err)
			var events []contract.AuditSummary
			for _, event := range page.Items {
				if event.Category == "catalog" && event.Action == "refresh" {
					events = append(events, event)
				}
			}
			require.Len(t, events, 2)
			require.NotEqual(t, events[0].Phase, events[1].Phase)
			require.Equal(t, events[0].CorrelationID, events[1].CorrelationID)
			require.NotEqual(t, events[0].ID, events[1].ID)
		})
	}
}

func installCatalogAuditWork(t *testing.T, fixture catalogAuditFixture) (context.Context, context.CancelFunc, *refreshWork) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	work := &refreshWork{candidate: fixture.candidate, cancel: cancel}
	fixture.coordinator.mu.Lock()
	fixture.coordinator.work[fixture.candidate.Server.ID] = work
	fixture.coordinator.candidates[fixture.candidate.Server.ID] = fixture.candidate
	fixture.coordinator.mu.Unlock()
	return ctx, cancel, work
}

func TestCatalogAuditAdmissionDoesNotRetryAmbiguousErrors(t *testing.T) {
	for name, failure := range map[string]error{
		"success":        nil,
		"text-lookalike": errors.New(storage.ErrMutationBusy.Error()),
		"wrapped":        fmt.Errorf("wrapped: %w", storage.ErrMutationBusy),
		"joined":         errors.Join(storage.ErrMutationBusy, errors.New("other failure")),
		"latched-busy":   errors.Join(storage.ErrMutationBusy, storage.ErrStorageLatched),
		"latched":        storage.ErrStorageLatched,
		"deadline":       context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newCatalogAuditFixture(t)
			ctx, _, work := installCatalogAuditWork(t, fixture)
			calls := 0
			err := fixture.coordinator.persistAudit(ctx, ctx, work, false, func(context.Context) error { calls++; return failure })
			require.Equal(t, failure, err)
			require.Equal(t, 1, calls)
			require.Zero(t, fixture.scheduler.count())
		})
	}
}

func TestCatalogAuditAdmissionHasExactlyFourAcquisitions(t *testing.T) {
	fixture := newCatalogAuditFixture(t)
	ctx, cancel, work := installCatalogAuditWork(t, fixture)
	var calls atomic.Int32
	results, done := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(done)
		results <- fixture.coordinator.persistAudit(ctx, ctx, work, false, func(context.Context) error { calls.Add(1); return storage.ErrMutationBusy })
	}()
	t.Cleanup(func() { cancel(); <-done })
	exhaustCatalogAuditWaits(t, fixture.scheduler)
	select {
	case err := <-results:
		require.ErrorIs(t, err, storage.ErrMutationBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("admission attempts did not exhaust")
	}
	require.EqualValues(t, 4, calls.Load())
	require.Equal(t, 3, fixture.scheduler.count())
}

func TestCatalogAuditAdmissionWaitHonorsOwnerFences(t *testing.T) {
	for _, settlement := range []bool{false, true} {
		for _, mode := range []string{"cancel", "shutdown", "candidate", "work"} {
			t.Run(fmt.Sprintf("%s/settlement-%t", mode, settlement), func(t *testing.T) {
				fixture := newCatalogAuditFixture(t)
				ctx, cancel, work := installCatalogAuditWork(t, fixture)
				var calls atomic.Int32
				results, done := make(chan error, 1), make(chan struct{})
				go func() {
					defer close(done)
					writeCtx := ctx
					if settlement {
						writeCtx = context.WithoutCancel(ctx)
					}
					results <- fixture.coordinator.persistAudit(ctx, writeCtx, work, settlement, func(context.Context) error { calls.Add(1); return storage.ErrMutationBusy })
				}()
				t.Cleanup(func() { cancel(); <-done })
				waitCatalogAuditTimer(t, fixture.scheduler, 0, 25*time.Millisecond)
				want := error(context.Canceled)
				switch mode {
				case "cancel":
					cancel()
				case "shutdown":
					fixture.coordinator.Shutdown()
				case "candidate", "work":
					// Acquiring the owner lock here also proves it is not held while waiting.
					fixture.coordinator.mu.Lock()
					if mode == "candidate" {
						replacement := fixture.candidate
						replacement.Generation++
						fixture.coordinator.candidates[replacement.Server.ID] = replacement
					} else {
						fixture.coordinator.work[fixture.candidate.Server.ID] = &refreshWork{candidate: fixture.candidate, cancel: func() {}}
					}
					fixture.coordinator.mu.Unlock()
					want = servers.ErrStaleRevision
					fixture.scheduler.fire(t, 0)
				}
				select {
				case err := <-results:
					require.ErrorIs(t, err, want)
				case <-time.After(5 * time.Second):
					t.Fatal("owner fence did not settle")
				}
				require.EqualValues(t, 1, calls.Load())
				require.Equal(t, 1, fixture.scheduler.count())
				require.True(t, fixture.scheduler.call(0).timer.stopped.Load())
			})
		}
	}
}

func TestCatalogAuditFinishKeepsOneSettlementDeadline(t *testing.T) {
	fixture := newCatalogAuditFixture(t)
	ctx, cancel, work := installCatalogAuditWork(t, fixture)
	ctx = audit.WithSystem(ctx)
	attempt, err := audit.NewAttempt(ctx, fixture.coordinator.clock.Now(), "catalog", "refresh", contract.AuditTarget{Type: "server", ID: fixture.candidate.Server.ID})
	require.NoError(t, err)
	require.NoError(t, audit.Append(ctx, fixture.coordinator.repository.store, attempt))
	release := holdCatalogAuditWriter(t, fixture.coordinator.repository.store)
	results, done := make(chan error, 1), make(chan struct{})
	go func() { defer close(done); results <- fixture.coordinator.finishAudit(ctx, work, attempt, "succeeded") }()
	t.Cleanup(func() { cancel(); <-done })
	waitCatalogAuditTimer(t, fixture.scheduler, 0, 25*time.Millisecond)
	fixture.scheduler.fire(t, 0)
	waitCatalogAuditTimer(t, fixture.scheduler, 1, 50*time.Millisecond)
	// Keep the injected backoff pending: the original settlement context must
	// end it even though the owner is still live and the timer never fires.
	select {
	case err := <-results:
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorIs(t, err, storage.ErrMutationBusy)
	case <-time.After(3 * time.Second):
		t.Fatal("settlement exceeded its original deadline")
	}
	require.NoError(t, ctx.Err())
	require.Equal(t, 2, fixture.scheduler.count())
	require.True(t, fixture.scheduler.call(1).timer.stopped.Load())
	release()
}

func TestCatalogAuditFinishPreservesFirstCancelledSettlement(t *testing.T) {
	fixture := newCatalogAuditFixture(t)
	ctx, cancel, work := installCatalogAuditWork(t, fixture)
	ctx = audit.WithSystem(ctx)
	attempt, err := audit.NewAttempt(ctx, fixture.coordinator.clock.Now(), "catalog", "refresh", contract.AuditTarget{Type: "server", ID: fixture.candidate.Server.ID})
	require.NoError(t, err)
	require.NoError(t, audit.Append(ctx, fixture.coordinator.repository.store, attempt))
	cancel()
	fixture.coordinator.Shutdown()
	require.NoError(t, fixture.coordinator.finishAudit(ctx, work, attempt, "unknown"))
	reader, err := audit.NewRepository(fixture.coordinator.repository.store)
	require.NoError(t, err)
	page, err := reader.List(t.Context(), audit.Query{Limit: 100})
	require.NoError(t, err)
	outcomes := 0
	for _, event := range page.Items {
		if event.Category == "catalog" && event.Action == "refresh" && event.Phase == "outcome" {
			outcomes++
			require.Equal(t, "unknown", event.Outcome)
			require.Equal(t, attempt.CorrelationID, event.CorrelationID)
		}
	}
	require.Equal(t, 1, outcomes)
	require.Zero(t, fixture.scheduler.count())
	// A distinct pending attempt gets one settlement chance, not waits, after
	// owner cancellation. Never invoke Finish again for acknowledged evidence.
	freshCtx := audit.WithSystem(t.Context())
	pending, err := audit.NewAttempt(freshCtx, fixture.coordinator.clock.Now(), "catalog", "refresh", contract.AuditTarget{Type: "server", ID: fixture.candidate.Server.ID})
	require.NoError(t, err)
	require.NoError(t, audit.Append(freshCtx, fixture.coordinator.repository.store, pending))
	release := holdCatalogAuditWriter(t, fixture.coordinator.repository.store)
	err = fixture.coordinator.finishAudit(ctx, work, pending, "unknown")
	require.ErrorIs(t, err, storage.ErrMutationBusy)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, fixture.scheduler.count())
	release()
}
