package runtimes

import (
	"context"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

// finishWork runs after the worker and its candidate defers have returned. The
// lifecycle lock covers terminal persistence, including mutation cleanup.
func (manager *Manager) finishWork(serverID string, work *reconciliationWork) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current == nil || current.work != work {
		return
	}
	work.returned = true
	displaced := work.displaced || work.cleanupOnly || current.generation != work.generation
	failureCause := diagnostics.Unavailable
	if !manager.draining && displaced && !work.settled && !work.failed {
		if current.blockedStop != nil {
			work.failed = true
		} else if err := manager.settleDisplaced(work); err != nil {
			work.failed = true
			if errors.Is(err, storage.ErrMutationBusy) {
				failureCause = diagnostics.Capacity
			}
		} else {
			work.settled = true
		}
	}
	if work.failed && !manager.draining {
		current.unsettled = work
		current.pending = false
		current.status.State = contract.RuntimeDegraded
		reason := contract.ReasonConnectivity
		if current.blockedStop != nil {
			reason = contract.ReasonStopUnconfirmed
			failureCause = diagnostics.Stopped
		}
		current.status.Reason = &reason
		current.status.RuntimeID = nil
		current.status.CatalogState = contract.ActiveCatalogUnavailable
		manager.observeUpstreamLocked(current, diagnostics.Facts{Event: diagnostics.ReconciliationSettlementFailure, Cause: failureCause})
	} else if work.cleanupOnly && !manager.draining {
		current.pending = true
	}
	if !manager.draining && (!displaced || work.failed) {
		manager.observeHealthLocked(current)
	}
	facts := diagnosticStatus(current)
	facts.Event = diagnostics.UpstreamAttemptComplete
	facts.Attempt = work.diagnosticAttempt
	facts.Duration = diagnostics.Elapsed(work.diagnosticStart, manager.diagnosticNow())
	if displaced {
		facts.Reason, facts.Disposition = diagnostics.ReasonSuperseded, diagnostics.DispositionSuperseded
	}
	if manager.draining {
		facts.Reason, facts.Disposition = diagnostics.ReasonCancelled, diagnostics.DispositionCancelled
	}
	manager.observeUpstreamLocked(current, facts)
	current.work = nil
	current.reconcileAttempt = nil
	manager.releaseLocked(current)
	if !manager.draining {
		manager.publish(contract.InvalidationServers, &serverID)
		manager.publish(contract.InvalidationSystemStatus, nil)
	}
}

func (manager *Manager) settleDisplaced(work *reconciliationWork) error {
	ctx := audit.WithSystem(audit.WithCause(context.Background(), work.cause))
	var event *contract.AuditEvent
	if work.attempt != nil {
		outcome, err := audit.Outcome(*work.attempt, time.Now(), "failed")
		if err != nil {
			return err
		}
		reason := contract.ReasonSuperseded
		outcome.Detail.Reason = &reason
		event = &outcome
	}
	if work.operationID == nil {
		if event == nil {
			return nil
		}
		return manager.repository.RecordReconciliation(ctx, *event)
	}
	_, changed, err := manager.repository.SettleDisplacedReconciliation(ctx, *work.operationID, event)
	if err == nil && changed {
		manager.publish(contract.InvalidationServerOperations, work.operationID)
	}
	return err
}
