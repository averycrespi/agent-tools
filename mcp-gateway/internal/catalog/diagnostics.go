package catalog

import (
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
)

// Poll results have no reconciliation owner. Observe them without invoking its
// completion callback or changing polling, retry, publication or fencing work.
func (coordinator *Coordinator) observePoll(candidate runtimes.Candidate, result runtimes.CatalogOutcome) {
	if coordinator.diagnostics == nil {
		return
	}
	if result.Reason != nil && (*result.Reason == contract.ReasonSuperseded || *result.Reason == contract.ReasonCancelled || *result.Reason == contract.ReasonInterrupted) {
		return
	}
	facts := diagnostics.Facts{Event: diagnostics.UpstreamUnhealthy, Phase: diagnostics.PhaseToolDiscovery, Reason: result.DiagnosticReason, Disposition: diagnostics.DispositionUnknown}
	if result.DiagnosticRetryDelay > 0 {
		facts.Disposition = diagnostics.DispositionRetryScheduled
	}
	if result.State == contract.ActiveCatalogCurrent {
		facts.Event, facts.Reason, facts.Disposition = diagnostics.UpstreamRecovered, diagnostics.ReasonNone, diagnostics.DispositionHealthy
	}
	if coordinator.diagnosticReference != nil {
		facts.Upstream = coordinator.diagnosticReference(candidate.Server.ID)
	}
	coordinator.diagnostics.Reconciliation(facts)
}

func (coordinator *Coordinator) observeSchedule(candidate runtimes.Candidate, result runtimes.CatalogOutcome) {
	if coordinator.diagnostics == nil || result.DiagnosticRetryDelay <= 0 {
		return
	}
	reason := result.DiagnosticReason
	if result.State == contract.ActiveCatalogCurrent {
		reason = diagnostics.ReasonNone
	}
	facts := diagnostics.Facts{Event: diagnostics.CatalogPollScheduled, Phase: diagnostics.PhaseToolDiscovery, Reason: reason, Disposition: diagnostics.DispositionRetryScheduled, Delay: result.DiagnosticRetryDelay}
	if coordinator.diagnosticReference != nil {
		facts.Upstream = coordinator.diagnosticReference(candidate.Server.ID)
	}
	coordinator.diagnostics.Reconciliation(facts)
}
