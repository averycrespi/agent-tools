package runtimes

import (
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
)

// The existing lifecycle lock owns these scalar observations. No diagnostic
// result is consumed by reconciliation, persistence, cancellation or authority.
func (manager *Manager) observeUpstreamLocked(current *entry, facts diagnostics.Facts) {
	if manager.diagnostics == nil || current == nil {
		return
	}
	facts.Upstream = current.diagnosticReference
	if facts.Attempt == 0 && current.work != nil {
		facts.Attempt = current.work.diagnosticAttempt
	}
	manager.diagnostics.Reconciliation(facts)
}

func (manager *Manager) startDiagnosticAttempt(serverID string, work *reconciliationWork) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	work.diagnosticStart = manager.diagnosticNow()
	current := manager.entries[serverID]
	if current != nil && current.diagnosticReference == 0 {
		current.diagnosticReference = manager.diagnosticReference(serverID)
	}
	manager.observeUpstreamLocked(current, diagnostics.Facts{Event: diagnostics.UpstreamAttemptStart, Attempt: work.diagnosticAttempt, Phase: diagnostics.PhaseReconciliation, Reason: diagnostics.ReasonNone, Disposition: diagnostics.DispositionExecuting})
}

func (manager *Manager) diagnosticPhase(serverID string, generation uint64, phase diagnostics.Phase, reason diagnostics.Reason, retryDelay time.Duration) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[serverID]
	if current != nil && current.work != nil && current.work.generation == generation {
		current.work.diagnosticPhase = phase
		current.work.diagnosticReason = reason
		current.work.diagnosticRetryDelay = retryDelay
	}
}

func diagnosticStatus(current *entry) diagnostics.Facts {
	facts := diagnostics.Facts{Phase: diagnostics.PhaseUnknown, Reason: diagnostics.PublicReason(current.status.Reason)}
	if current.work != nil {
		facts.Phase = current.work.diagnosticPhase
		if current.work.diagnosticReason != diagnostics.ReasonUnknown || facts.Phase == diagnostics.PhaseConnection || facts.Phase == diagnostics.PhaseInitialization || facts.Phase == diagnostics.PhaseToolDiscovery {
			facts.Reason = current.work.diagnosticReason
		}
	}
	switch {
	case current.blockedStop != nil || current.status.Reason != nil && *current.status.Reason == contract.ReasonStopUnconfirmed:
		facts.Phase, facts.Reason, facts.Disposition = diagnostics.PhaseCleanup, diagnostics.ReasonStopUnconfirmed, diagnostics.DispositionCleanupUncertain
	case current.status.State == contract.RuntimeAuthenticationRequired:
		facts.Disposition = diagnostics.DispositionOperatorAuthentication
	case current.status.State == contract.RuntimeRetryWait || current.timer != nil || current.work != nil && current.work.diagnosticRetryDelay > 0 && current.status.CatalogState != contract.ActiveCatalogCurrent:
		facts.Disposition = diagnostics.DispositionRetryScheduled
	case current.status.State == contract.RuntimeActive && current.status.CatalogState == contract.ActiveCatalogCurrent:
		facts.Reason, facts.Disposition = diagnostics.ReasonNone, diagnostics.DispositionHealthy
	case current.status.State == contract.RuntimeInactive || current.status.State == contract.RuntimeDeleted || current.status.State == contract.RuntimeDegraded && current.active == nil && current.activating == nil:
		facts.Disposition = diagnostics.DispositionStopped
	default:
		facts.Disposition = diagnostics.DispositionUnknown
	}
	return facts
}

func (manager *Manager) observeCatalog(candidate Candidate, outcome CatalogOutcome) {
	// The traversal owner observes after its scheduling decision; joined callers
	// keep their existing completion timing without duplicating that observation.
	if outcome.DiagnosticJoined {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	current := manager.entries[candidate.Server.ID]
	if manager.draining || current == nil || current.generation != candidate.Generation {
		return
	}
	facts := diagnostics.Facts{Event: diagnostics.UpstreamUnhealthy, Phase: diagnostics.PhaseToolDiscovery, Reason: outcome.DiagnosticReason, Disposition: diagnostics.DispositionUnknown}
	switch {
	case outcome.OAuthChallenge != nil:
		facts.Reason = diagnostics.ReasonAuthenticationRejected
		if outcome.OAuthChallenge.Kind == downstream.OAuthChallengeStepUp {
			facts.Disposition = diagnostics.DispositionOperatorAuthentication
		}
	case outcome.State == contract.ActiveCatalogCurrent:
		facts.Event, facts.Reason, facts.Disposition = diagnostics.UpstreamRecovered, diagnostics.ReasonNone, diagnostics.DispositionHealthy
	case outcome.DiagnosticRetryDelay > 0:
		facts.Disposition = diagnostics.DispositionRetryScheduled
	}
	manager.observeUpstreamLocked(current, facts)
}

func (manager *Manager) observeHealthLocked(current *entry) {
	if current == nil {
		return
	}
	facts := diagnosticStatus(current)
	switch {
	case facts.Disposition == diagnostics.DispositionHealthy:
		facts.Event = diagnostics.UpstreamRecovered
	case current.status.State == contract.RuntimeAuthenticationRequired, current.status.State == contract.RuntimeDegraded, current.status.State == contract.RuntimeRetryWait, current.status.State == contract.RuntimeActive && current.status.CatalogState != contract.ActiveCatalogCurrent:
		facts.Event = diagnostics.UpstreamUnhealthy
	default:
		return
	}
	manager.observeUpstreamLocked(current, facts)
}
