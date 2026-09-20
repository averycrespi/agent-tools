package diagnostics

// Guidance uses only validated Gateway facts. It never accepts upstream text
// and never instructs execution replay, including for retry-scheduled work.
func guidance(f Facts) string {
	switch f.Event {
	case UpstreamRecovered:
		return "no_action"
	case DurabilityFailure, StorageLatch:
		return "storage_recovery"
	case ReconciliationSettlementFailure:
		return "inspect_settlement_no_replay"
	case Loss:
		return "inspect_diagnostic_sink"
	}
	if upstreamEvent(f.Event) {
		if f.Disposition == DispositionCleanupUncertain || f.Reason == ReasonStopUnconfirmed || f.Reason == ReasonCleanupPending {
			return "inspect_settlement_no_replay"
		}
		if f.Disposition == DispositionRetryScheduled {
			return "wait_scheduled_retry"
		}
		if f.Disposition == DispositionOperatorAuthentication {
			return "authorize_upstream"
		}
		switch f.Reason {
		case ReasonAuthenticationRequired, ReasonAuthenticationRejected, ReasonOAuthRejected, ReasonOAuthExpired, ReasonRegistrationExpired:
			return "authorize_upstream"
		case ReasonCredentialAbsent, ReasonKeyringUnavailable, ReasonKeyringLocked, ReasonKeyringInteractionRequired:
			return "inspect_credentials"
		case ReasonConnectivity, ReasonTLSFailed, ReasonTimeout, ReasonProcessExited:
			return "inspect_connection"
		case ReasonConfigurationInvalid, ReasonProtocolInvalid, ReasonProtocolUnsupported, ReasonResourceLimit, ReasonOutputLimit, ReasonCatalogInvalid, ReasonCatalogLimit, ReasonCatalogStale:
			return "inspect_configuration"
		}
	}
	return "inspect_status"
}
