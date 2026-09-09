package runtimes

import (
	"context"
	"errors"
	"sync"
	"testing"

	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

func (repository *fakeRepository) SettleDisplacedReconciliation(ctx context.Context, id string, event *contract.AuditEvent) (servers.Operation, bool, error) {
	repository.mu.Lock()
	hook := repository.beforeTransition
	repository.mu.Unlock()
	if hook != nil {
		if err := hook(contract.OperationSuperseded); err != nil {
			return servers.Operation{}, false, err
		}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	operation, ok := repository.operations[id]
	if !ok {
		return servers.Operation{}, false, servers.ErrNotFound
	}
	changed := operation.State == contract.OperationScheduled || operation.State == contract.OperationRunning
	if changed {
		reason := contract.ReasonSuperseded
		operation.State, operation.Reason = contract.OperationSuperseded, &reason
		repository.operations[id] = operation
		repository.transitions <- operation
	}
	if event != nil {
		repository.audits = append(repository.audits, *event)
	}
	return operation, changed, nil
}

func TestOAuthDisplacementSettlesOriginalOperation(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	var serverID string
	for serverID = range repository.servers {
		break
	}
	operation, err := repository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.Trigger(serverID, &operation.Operation.ID, true)
	original := receiveCandidate(t, driver.started)
	manager.Trigger(serverID, nil, true)
	driver.startResult <- activeOutcome()
	require.Equal(t, original.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	before, err := repository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationRunning, before.State)
	driver.stopResult <- true
	replacement := receiveCandidate(t, driver.started)
	require.Nil(t, replacement.OperationID)
	driver.startResult <- activeOutcome()
	waitSettlementWorkers(t, manager)
	require.Equal(t, contract.RuntimeActive, manager.Status(serverID).State)
	require.Zero(t, manager.Status(serverID).Reconciliation.InUse)
	settled, err := repository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	// Release the fixture's active handle before assertions can terminate the test.
	driver.stopResult <- true
	manager.Shutdown()
	require.Equal(t, contract.OperationSuperseded, settled.State)
	require.Equal(t, operation.Operation.ID, settled.ID)
}

func waitSettlementWorkers(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.True(t, manager.Wait(ctx), "reconciliation did not return")
}

type settlementObserver struct{ facts []diagnostics.Facts }

func (observer *settlementObserver) Reconciliation(facts diagnostics.Facts) {
	observer.facts = append(observer.facts, facts)
}

func TestOAuthDisplacementFailureAndTerminalRaces(t *testing.T) {
	for _, scenario := range []string{"concurrent callbacks", "terminal winner", "stop unconfirmed", "persistence busy", "persistence failed", "drain"} {
		t.Run(scenario, func(t *testing.T) {
			repository := newFakeRepository(1)
			driver := newLifecycleDriver()
			observer := new(settlementObserver)
			manager, err := New(Options{Repository: repository, Driver: driver, Diagnostics: observer})
			require.NoError(t, err)
			t.Cleanup(func() { driver.stopResult <- true; manager.Shutdown() })
			var serverID string
			for serverID = range repository.servers {
				break
			}
			operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationActivate, ExpectedDesiredRevision: "1"})
			require.NoError(t, err)
			manager.Trigger(serverID, &operation.Operation.ID, true)
			first := receiveCandidate(t, driver.started)
			var calls sync.WaitGroup
			for range 3 {
				calls.Add(1)
				go func() { defer calls.Done(); manager.Trigger(serverID, nil, true) }()
			}
			calls.Wait()
			if scenario == "terminal winner" {
				_, err = repository.TransitionOperation(t.Context(), operation.Operation.ID, contract.OperationFailed, nil)
				require.NoError(t, err)
			}
			if scenario == "persistence busy" || scenario == "persistence failed" {
				repository.mu.Lock()
				repository.beforeTransition = func(state contract.ServerOperationState) error {
					if state != contract.OperationSuperseded {
						return nil
					}
					if scenario == "persistence busy" {
						return storage.ErrMutationBusy
					}
					return errors.New("private failure must not enter diagnostics")
				}
				repository.mu.Unlock()
			}
			if scenario == "drain" {
				<-manager.Drain(t.Context())
			}
			driver.startResult <- activeOutcome()
			require.Equal(t, first.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
			driver.stopResult <- scenario != "stop unconfirmed"
			healthy := scenario == "concurrent callbacks" || scenario == "terminal winner"
			if healthy {
				replacement := receiveCandidate(t, driver.started)
				require.Nil(t, replacement.OperationID)
				driver.startResult <- activeOutcome()
			}
			waitSettlementWorkers(t, manager)
			current, err := repository.GetOperation(t.Context(), operation.Operation.ID)
			require.NoError(t, err)
			expected := contract.OperationRunning
			if healthy {
				expected = contract.OperationSuperseded
			}
			if scenario == "terminal winner" {
				expected = contract.OperationFailed
			}
			require.Equal(t, expected, current.State)
			require.Zero(t, manager.AdmissionStatus().InUse)
			manager.mu.Lock()
			retained := manager.entries[serverID].unsettled
			manager.mu.Unlock()
			if !healthy && scenario != "drain" {
				require.NotNil(t, retained)
				require.Equal(t, operation.Operation.ID, *retained.operationID)
				require.NotNil(t, retained.attempt)
				require.Equal(t, diagnostics.ReconciliationSettlementFailure, observer.facts[len(observer.facts)-1].Event)
				manager.Trigger(serverID, nil, true)
			}
			select {
			case unexpected := <-driver.started:
				t.Fatalf("unexpected replay: %s", unexpected.RuntimeID)
			default:
			}
			displaced := 0
			for _, fact := range observer.facts {
				if fact.Event == diagnostics.ReconciliationDisplaced {
					displaced++
				}
			}
			require.Equal(t, 1, displaced)
			if healthy {
				repository.mu.Lock()
				audits := append([]contract.AuditEvent(nil), repository.audits...)
				repository.mu.Unlock()
				require.Len(t, audits, 4)
				require.Equal(t, "outcome", audits[1].Phase)
				require.Equal(t, audits[0].Target, audits[1].Target)
				require.Equal(t, contract.ReasonSuperseded, *audits[1].Detail.Reason)
				require.Equal(t, audits[0].CorrelationID, audits[1].CorrelationID)
			}
		})
	}
}

func TestOAuthQueuedDisplacementWaitsForCleanup(t *testing.T) {
	repository := newFakeRepository(1)
	driver := newLifecycleDriver()
	manager, err := New(Options{Repository: repository, Driver: driver})
	require.NoError(t, err)
	t.Cleanup(func() { driver.stopResult <- true; manager.Shutdown() })
	var serverID string
	for serverID = range repository.servers {
		break
	}
	manager.Trigger(serverID, nil, true)
	original := receiveCandidate(t, driver.started)
	driver.startResult <- activeOutcome()
	waitSettlementWorkers(t, manager)
	operation, err := repository.CreateOperation(t.Context(), servers.OperationRequest{ServerID: serverID, Kind: contract.OperationReload, ExpectedDesiredRevision: "1"})
	require.NoError(t, err)
	manager.mu.Lock()
	manager.globalLimit = 0
	manager.mu.Unlock()
	manager.Trigger(serverID, &operation.Operation.ID, true)
	manager.Trigger(serverID, nil, true)
	manager.Trigger(serverID, nil, true)
	manager.mu.Lock()
	manager.globalLimit = 1
	manager.startAvailableLocked()
	manager.mu.Unlock()
	require.Equal(t, original.RuntimeID, receiveCandidate(t, driver.stopping).RuntimeID)
	before, err := repository.GetOperation(t.Context(), operation.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationScheduled, before.State)
	driver.stopResult <- true
	replacement := receiveCandidate(t, driver.started)
	require.Nil(t, replacement.OperationID)
	settled, err := repository.GetOperation(t.Context(), operation.Operation.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationSuperseded, settled.State)
	driver.startResult <- activeOutcome()
	waitSettlementWorkers(t, manager)
	require.Equal(t, contract.RuntimeActive, manager.Status(serverID).State)
}
