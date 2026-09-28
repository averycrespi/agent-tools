package runtimes

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lostCatalogOutcome(err error) CatalogOutcome {
	failure := ClassifyFailure(err)
	return CatalogOutcome{State: contract.ActiveCatalogUnavailable, Intent: CatalogTraversalPoll, RuntimeHealth: CatalogRuntimeLost, RuntimeFailure: &failure, Reason: &failure.Reason}
}

func TestCatalogCompletionAndExplicitRefreshConsumeRuntimeLossOnce(t *testing.T) {
	repository := newFakeRepository(1)
	driver, publisher, scheduler := newLifecycleDriver(), newRecordingPublisher(), newFakeScheduler()
	catalog := newRefreshCatalog()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler, Catalog: catalog})
	require.NoError(t, err)
	defer manager.Shutdown()
	var id string
	for id = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, id)
	operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: id, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.Trigger(id, &operation.Operation.ID, false)
	require.Equal(t, active.Key(), receiveCandidate(t, catalog.refreshStarted).Key())
	outcome := lostCatalogOutcome(downstream.ErrSessionLost)
	stale := active
	stale.Generation++
	require.False(t, manager.HandleCatalogCompletion(stale, outcome, nil))
	var completions sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		completions.Go(func() { <-start; manager.HandleCatalogCompletion(active, outcome, &operation.Operation.ID) })
	}
	completions.Go(func() { <-start; joined := outcome; joined.DiagnosticJoined = true; catalog.release <- joined })
	close(start)
	completions.Wait()
	require.Equal(t, active.Key(), receiveCandidate(t, driver.stopping).Key())
	assert.Equal(t, contract.RuntimeStopping, manager.Status(id).State)
	select {
	case <-scheduler.notify:
		t.Fatal("retry scheduled before verified stop")
	default:
	}
	driver.stopResult <- true
	require.Eventually(t, func() bool { return manager.Status(id).State == contract.RuntimeRetryWait }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		op, getErr := repository.GetOperation(t.Context(), operation.Operation.ID)
		return getErr == nil && op.State == contract.OperationFailed
	}, time.Second, time.Millisecond)
	assert.Equal(t, time.Second, scheduler.fire(t, 0))
	fresh := receiveCandidate(t, driver.started)
	assert.NotEqual(t, active.RuntimeID, fresh.RuntimeID)
	driver.startResult <- activeOutcome()
	require.Eventually(t, func() bool {
		return manager.Status(id).State == contract.RuntimeActive && manager.Status(id).Reconciliation.InUse == 0
	}, time.Second, time.Millisecond)
	assert.False(t, manager.HandleCatalogCompletion(active, outcome, nil))
	assert.Equal(t, fresh.RuntimeID, *manager.Status(id).RuntimeID)
	select {
	case duplicate := <-driver.stopping:
		t.Fatalf("duplicate stop for %s", duplicate.RuntimeID)
	default:
	}
	select {
	case <-scheduler.notify:
		t.Fatal("duplicate recovery timer")
	default:
	}
}

func TestCatalogCompletionCannotResurrectRetiredRuntime(t *testing.T) {
	for _, action := range []string{"disable", "delete", "drain"} {
		t.Run(action, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver, publisher, scheduler := newLifecycleDriver(), newRecordingPublisher(), newFakeScheduler()
			manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler})
			require.NoError(t, err)
			defer manager.Shutdown()
			var id string
			for id = range repository.servers {
				break
			}
			active := establishActiveRuntime(t, manager, driver, publisher, id)
			var drained <-chan DrainResult
			expected := contract.RuntimeInactive
			switch action {
			case "drain":
				drained = manager.Drain(t.Context())
			case "disable":
				repository.setDesiredState(id, contract.DesiredServerDisabled)
				repository.setRevision(id, "2")
				manager.Trigger(id, nil, true)
			case "delete":
				repository.setDesiredState(id, contract.DesiredServerDeleted)
				repository.setRevision(id, "2")
				manager.Trigger(id, nil, true)
				expected = contract.RuntimeDeleted
			}
			require.Equal(t, active.Key(), receiveCandidate(t, driver.stopping).Key())
			assert.False(t, manager.HandleCatalogCompletion(active, lostCatalogOutcome(downstream.ErrTransportClosed), nil))
			driver.stopResult <- true
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if drained != nil {
				select {
				case result := <-drained:
					assert.Zero(t, result.Unconfirmed)
				case <-ctx.Done():
					t.Fatal("drain did not settle")
				}
			} else {
				require.True(t, manager.Wait(ctx))
				assert.Equal(t, expected, manager.Status(id).State)
			}
			assert.False(t, manager.HandleCatalogCompletion(active, lostCatalogOutcome(downstream.ErrSessionLost), nil))
			assert.Nil(t, manager.Status(id).RuntimeID)
			select {
			case replacement := <-driver.started:
				t.Fatalf("retired runtime resurrected: %s", replacement.RuntimeID)
			default:
			}
			select {
			case <-scheduler.notify:
				t.Fatal("retired completion scheduled retry")
			default:
			}
		})
	}
}

func TestCatalogCompletionCannotDisplaceQueuedRetirement(t *testing.T) {
	for _, desired := range []contract.DesiredServerState{contract.DesiredServerDisabled, contract.DesiredServerDeleted} {
		t.Run(string(desired), func(t *testing.T) {
			repository := newFakeRepository(5)
			driver, publisher, scheduler := newLifecycleDriver(), newRecordingPublisher(), newFakeScheduler()
			manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler})
			require.NoError(t, err)
			defer manager.Shutdown()
			var id string
			for id = range repository.servers {
				break
			}
			active := establishActiveRuntime(t, manager, driver, publisher, id)
			// Keep all four reconciliation slots occupied so retirement is queued
			// with the old active handle still retained for its stop owner.
			for other := range repository.servers {
				if other == id {
					continue
				}
				manager.Trigger(other, nil, true)
				receiveCandidate(t, driver.started)
			}
			require.True(t, manager.AdmissionStatus().Saturated)
			repository.setDesiredState(id, desired)
			repository.setRevision(id, "2")
			manager.Trigger(id, nil, true)
			manager.mu.Lock()
			retained := manager.entries[id].active != nil && manager.entries[id].pending && manager.entries[id].generation != active.Generation
			generation := manager.entries[id].generation
			manager.mu.Unlock()
			require.True(t, retained, "fixture missed the queued pre-stop window")
			require.False(t, manager.HandleCatalogCompletion(active, lostCatalogOutcome(downstream.ErrSessionLost), nil))
			manager.mu.Lock()
			after := manager.entries[id].generation
			manager.mu.Unlock()
			assert.Equal(t, generation, after, "stale loss displaced retirement")
			select {
			case <-driver.stopping:
				t.Fatal("stale loss launched a competing stop")
			default:
			}
			select {
			case <-scheduler.notify:
				t.Fatal("stale loss scheduled retry")
			default:
			}
			driver.startResult <- activeOutcome()
			require.Equal(t, active.Key(), receiveCandidate(t, driver.stopping).Key())
			driver.stopResult <- true
			for range 3 {
				driver.startResult <- activeOutcome()
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			require.True(t, manager.Wait(ctx))
			expected := contract.RuntimeInactive
			if desired == contract.DesiredServerDeleted {
				expected = contract.RuntimeDeleted
			}
			assert.Equal(t, expected, manager.Status(id).State)
			assert.Nil(t, manager.Status(id).RuntimeID)
			select {
			case fresh := <-driver.started:
				t.Fatalf("retired runtime restarted: %s", fresh.RuntimeID)
			default:
			}
		})
	}
}

func TestCatalogCompletionPreservesNonretryableAuthenticationLoss(t *testing.T) {
	repository := newFakeRepository(1)
	driver, publisher, scheduler := newLifecycleDriver(), newRecordingPublisher(), newFakeScheduler()
	manager, err := New(Options{Repository: repository, Driver: driver, Publisher: publisher, Scheduler: scheduler})
	require.NoError(t, err)
	defer manager.Shutdown()
	var id string
	for id = range repository.servers {
		break
	}
	active := establishActiveRuntime(t, manager, driver, publisher, id)
	require.True(t, manager.HandleCatalogCompletion(active, lostCatalogOutcome(downstream.ErrAuthenticationRejected), nil))
	require.Equal(t, active.Key(), receiveCandidate(t, driver.stopping).Key())
	driver.stopResult <- true
	require.Eventually(t, func() bool { return manager.Status(id).State == contract.RuntimeAuthenticationRequired }, time.Second, time.Millisecond)
	assert.Equal(t, contract.ServerCredentialUnavailable, manager.Status(id).CredentialState)
	select {
	case <-scheduler.notify:
		t.Fatal("nonretryable catalog loss scheduled retry")
	default:
	}
}
