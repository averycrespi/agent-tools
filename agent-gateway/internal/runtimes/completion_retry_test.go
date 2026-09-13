package runtimes

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCompletionContentionDoesNotReplayActivation(t *testing.T) {
	repository := newFakeRepository(1)
	driver := &outcomeDriver{calls: make(chan Candidate, 8), outcomes: []Outcome{activeOutcome()}}
	manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}})
	require.NoError(t, err)
	t.Cleanup(manager.Shutdown)
	var serverID string
	for serverID = range repository.servers {
		break
	}
	operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	attempts := 0
	repository.beforeTransition = func(state contract.ServerOperationState) error {
		if state == contract.OperationSucceeded {
			attempts++
			if attempts <= 2 {
				return fmt.Errorf("admission refused: %w", storage.ErrMutationBusy)
			}
		}
		return nil
	}
	manager.Trigger(serverID, &operation.Operation.ID, true)
	waitSettlementWorkers(t, manager)
	current, err := repository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationSucceeded, current.State)
	require.Equal(t, contract.RuntimeActive, manager.Status(serverID).State)
	require.Equal(t, contract.ActiveCatalogCurrent, manager.Status(serverID).CatalogState)
	require.Zero(t, manager.Status(serverID).Reconciliation.InUse)
	require.Len(t, driver.calls, 1, "completion must not replay startup")
	require.Len(t, repository.audits, 2, "one attempt and one outcome")
	require.Equal(t, repository.audits[0].CorrelationID, repository.audits[1].CorrelationID)
}

func TestCompletionRefusalBoundsAndUncertainFailures(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		err      error
		attempts int
		cause    diagnostics.Cause
	}{
		{"busy", storage.ErrMutationBusy, 4, diagnostics.Capacity},
		{"unavailable", errors.New("uncertain persistence"), 1, diagnostics.Unavailable},
		{"latched busy", errors.Join(storage.ErrMutationBusy, storage.ErrStorageLatched), 1, diagnostics.Unavailable},
		{"generic capacity", servers.ErrResourceLimit, 1, diagnostics.Unavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := &outcomeDriver{calls: make(chan Candidate, 8), outcomes: []Outcome{activeOutcome()}}
			observer := &settlementObserver{}
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Diagnostics: observer})
			require.NoError(t, err)
			t.Cleanup(manager.Shutdown)
			var id string
			for id = range repository.servers {
				break
			}
			operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: id, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
			require.NoError(t, err)
			attempts := 0
			repository.beforeTransition = func(state contract.ServerOperationState) error {
				if state == contract.OperationSucceeded {
					attempts++
					return scenario.err
				}
				return nil
			}
			manager.Trigger(id, &operation.Operation.ID, true)
			waitSettlementWorkers(t, manager)
			require.Equal(t, scenario.attempts, attempts)
			current, err := repository.GetOperation(t.Context(), operation.Operation.ID)
			require.NoError(t, err)
			require.Equal(t, contract.OperationRunning, current.State)
			require.Equal(t, contract.RuntimeDegraded, manager.Status(id).State)
			require.Nil(t, manager.Status(id).RuntimeID)
			require.Zero(t, manager.AdmissionStatus().InUse)
			failures := 0
			for _, fact := range observer.facts {
				if fact.Event == diagnostics.ReconciliationSettlementFailure {
					failures++
					require.Equal(t, scenario.cause, fact.Cause)
				}
				if fact.Event == diagnostics.UpstreamAttemptComplete {
					require.Equal(t, diagnostics.DispositionStopped, fact.Disposition)
				}
			}
			require.Equal(t, 1, failures)
			manager.Trigger(id, nil, true)
			waitSettlementWorkers(t, manager)
			require.Len(t, driver.calls, 1, "retained uncertain or exhausted work must not replay")
		})
	}
}

func TestCompletionContentionForFailedAndOperationlessOutcomes(t *testing.T) {
	for _, operationless := range []bool{false, true} {
		t.Run(fmt.Sprint(operationless), func(t *testing.T) {
			repository := newFakeRepository(1)
			reason := contract.ReasonConfigurationInvalid
			driver := &outcomeDriver{calls: make(chan Candidate, 8), outcomes: []Outcome{{State: contract.RuntimeDegraded, Reason: &reason}}}
			manager, err := New(Options{Repository: repository, Driver: driver})
			require.NoError(t, err)
			t.Cleanup(manager.Shutdown)
			var id string
			for id = range repository.servers {
				break
			}
			var operationID *string
			if !operationless {
				operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: id, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
				require.NoError(t, err)
				operationID = &operation.Operation.ID
			}
			attempts := 0
			refuse := func() error {
				attempts++
				if attempts == 1 {
					return storage.ErrMutationBusy
				}
				return nil
			}
			if operationless {
				repository.beforeAudit = func(event contract.AuditEvent) error {
					if event.Phase == "outcome" {
						return refuse()
					}
					return nil
				}
			} else {
				repository.beforeTransition = func(state contract.ServerOperationState) error {
					if state == contract.OperationFailed {
						return refuse()
					}
					return nil
				}
			}
			manager.Trigger(id, operationID, true)
			waitSettlementWorkers(t, manager)
			require.Equal(t, 2, attempts)
			require.Equal(t, contract.RuntimeDegraded, manager.Status(id).State)
			require.Equal(t, reason, *manager.Status(id).Reason)
			require.Len(t, repository.audits, 2)
			require.Len(t, driver.calls, 1)
			if operationID != nil {
				operation, err := repository.GetOperation(t.Context(), *operationID)
				require.NoError(t, err)
				require.Equal(t, contract.OperationFailed, operation.State)
			}
		})
	}
}

func TestCompletionWaitRevalidatesLifecycle(t *testing.T) {
	for _, action := range []string{"drain", "replace", "runtime failure"} {
		t.Run(action, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := &outcomeDriver{calls: make(chan Candidate, 8), outcomes: []Outcome{activeOutcome(), activeOutcome()}}
			scheduler := newFakeScheduler()
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Scheduler: scheduler})
			require.NoError(t, err)
			t.Cleanup(manager.Shutdown)
			var id string
			for id = range repository.servers {
				break
			}
			operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: id, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
			require.NoError(t, err)
			repository.beforeTransition = func(state contract.ServerOperationState) error {
				if state == contract.OperationSucceeded {
					return storage.ErrMutationBusy
				}
				return nil
			}
			manager.Trigger(id, &operation.Operation.ID, true)
			candidate := receiveCandidate(t, driver.calls)
			select {
			case <-scheduler.notify:
			case <-time.After(time.Second):
				t.Fatal("completion did not wait")
			}
			// Reads and lifecycle changes must acquire the lock while admission waits.
			require.EqualValues(t, 1, manager.Status(id).Reconciliation.InUse)
			switch action {
			case "drain":
				<-manager.Drain(t.Context())
			case "replace":
				manager.Trigger(id, nil, true)
			case "runtime failure":
				require.True(t, manager.ReportRuntimeFailure(candidate, FailureDisposition{State: contract.RuntimeDegraded, Reason: contract.ReasonConnectivity, RuntimeLost: true}))
			}
			repository.mu.Lock()
			repository.beforeTransition = nil
			repository.mu.Unlock()
			if action != "drain" {
				scheduler.mu.Lock()
				timer := scheduler.calls[0].timer
				scheduler.mu.Unlock()
				timer.callback()
			}
			waitSettlementWorkers(t, manager)
			current, err := repository.GetOperation(t.Context(), operation.Operation.ID)
			require.NoError(t, err)
			require.NotEqual(t, contract.OperationSucceeded, current.State, "old success must not cross the lifecycle fence")
			if action == "drain" {
				require.Equal(t, contract.OperationRunning, current.State)
			} else {
				require.Equal(t, contract.OperationSuperseded, current.State)
			}
			require.Zero(t, manager.AdmissionStatus().InUse)
		})
	}
}
