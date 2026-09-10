package diagnostics

import (
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

// References are counters unrelated to resource identity or audit entropy. Zero
// denotes unavailable correlation; saturation never reuses an earlier reference.
var upstreamReferences atomic.Uint64
var attemptReferences atomic.Uint64

func UpstreamReference() uint64 { return NextID(&upstreamReferences) }
func AttemptReference() uint64  { return NextID(&attemptReferences) }

type Phase uint8

const (
	PhaseUnknown Phase = iota
	PhaseReconciliation
	PhaseConnection
	PhaseInitialization
	PhaseToolDiscovery
	PhaseCredentials
	PhaseAuthorization
	PhaseRefresh
	PhaseCleanup
	PhaseOAuthMetadata
	PhaseOAuthRegistration
	PhaseOAuthCallback
	PhaseOAuthExchange
	PhaseOAuthInstallation
)

var phaseNames = [...]string{"unknown", "reconciliation", "connection", "mcp_initialization", "tool_discovery", "credential_resolution", "oauth_authorization", "oauth_refresh", "cleanup", "oauth_metadata", "oauth_registration", "oauth_callback", "oauth_exchange", "oauth_installation"}

type Reason uint8

const (
	ReasonUnknown Reason = iota
	ReasonNone
	ReasonConnectivity
	ReasonAuthenticationRequired
	ReasonAuthenticationRejected
	ReasonProtocolInvalid
	ReasonProtocolUnsupported
	ReasonResourceLimit
	ReasonConfigurationInvalid
	ReasonKeyringUnavailable
	ReasonStopUnconfirmed
	ReasonTimeout
	ReasonCancelled
	ReasonSuperseded
	ReasonOAuthRejected
	ReasonCleanupPending
	ReasonTLSFailed
	ReasonCredentialAbsent
	ReasonKeyringLocked
	ReasonKeyringInteractionRequired
	ReasonOAuthExpired
	ReasonRegistrationExpired
	ReasonProcessExited
	ReasonOutputLimit
	ReasonCatalogInvalid
	ReasonCatalogLimit
	ReasonCatalogStale
)

var reasonNames = [...]string{"unknown", "none", "connectivity", "authentication_required", "authentication_rejected", "protocol_invalid", "protocol_unsupported", "resource_limit", "configuration_invalid", "keyring_unavailable", "stop_unconfirmed", "timeout", "cancelled", "superseded", "oauth_rejected", "cleanup_pending", "tls_failed", "credential_absent", "keyring_locked", "keyring_interaction_required", "oauth_expired", "registration_expired", "process_exited", "output_limit", "catalog_invalid", "catalog_limit", "catalog_stale"}

// Only an explicit public reason is projected; arbitrary strings never survive.
func PublicReason(reason *contract.PublicReason) Reason {
	if reason == nil {
		return ReasonUnknown
	}
	switch *reason {
	case contract.ReasonConnectivity:
		return ReasonConnectivity
	case contract.ReasonAuthenticationRejected:
		return ReasonAuthenticationRejected
	case contract.ReasonProtocolInvalid:
		return ReasonProtocolInvalid
	case contract.ReasonProtocolUnsupported:
		return ReasonProtocolUnsupported
	case contract.ReasonResourceLimit:
		return ReasonResourceLimit
	case contract.ReasonConfigurationInvalid:
		return ReasonConfigurationInvalid
	case contract.ReasonKeyringUnavailable:
		return ReasonKeyringUnavailable
	case contract.ReasonStopUnconfirmed:
		return ReasonStopUnconfirmed
	case contract.ReasonSuperseded:
		return ReasonSuperseded
	case contract.ReasonOAuthRejected:
		return ReasonOAuthRejected
	case contract.ReasonCleanupPending:
		return ReasonCleanupPending
	case contract.ReasonTLSFailed:
		return ReasonTLSFailed
	case contract.ReasonCredentialAbsent, contract.ReasonKeyringAbsent:
		return ReasonCredentialAbsent
	case contract.ReasonKeyringLocked:
		return ReasonKeyringLocked
	case contract.ReasonKeyringInteractionRequired:
		return ReasonKeyringInteractionRequired
	case contract.ReasonKeyringUnsupported:
		return ReasonKeyringUnavailable
	case contract.ReasonOAuthExpired:
		return ReasonOAuthExpired
	case contract.ReasonRegistrationExpired:
		return ReasonRegistrationExpired
	case contract.ReasonProcessExited:
		return ReasonProcessExited
	case contract.ReasonOutputLimit:
		return ReasonOutputLimit
	case contract.ReasonCatalogInvalid:
		return ReasonCatalogInvalid
	case contract.ReasonCatalogLimit:
		return ReasonCatalogLimit
	case contract.ReasonCatalogStale:
		return ReasonCatalogStale
	case contract.ReasonCancelled, contract.ReasonInterrupted:
		return ReasonCancelled
	default:
		return ReasonUnknown
	}
}

type Disposition uint8

const (
	DispositionUnknown Disposition = iota
	DispositionExecuting
	DispositionRetryScheduled
	DispositionOperatorAuthentication
	DispositionHealthy
	DispositionStopped
	DispositionCleanupUncertain
	DispositionSuperseded
	DispositionCancelled
)

var dispositionNames = [...]string{"unknown", "executing", "retry_scheduled", "operator_authentication_required", "healthy", "stopped", "cleanup_uncertain", "superseded", "cancelled"}

func Elapsed(start, end time.Time) time.Duration {
	duration := end.Sub(start)
	if duration < 0 {
		return 0
	}
	return min(duration, contract.DiagnosticElapsedMaximum)
}

func upstreamEvent(event Event) bool {
	return event >= UpstreamAttemptStart && event <= CatalogPollScheduled
}

func upstreamLevel(event Event) Level {
	switch event {
	case UpstreamUnhealthy, OAuthExpired, OAuthFailed, OAuthRefreshFailed:
		return Warn
	case UpstreamRecovered, OAuthRequired, OAuthCompleted:
		return Info
	default:
		return Debug
	}
}

func validUpstream(f Facts) bool {
	if f.Cause != None || f.Stage != NoStage || f.Writer != Foreign || f.Call != 0 || f.Mutation != 0 || f.InvocationID != "" || f.Owned != 0 || f.Waiting != 0 || f.Limit != 0 || f.Suppressed != 0 {
		return false
	}
	maximumDelay := time.Minute
	if f.Event == CatalogPollScheduled {
		maximumDelay = contract.CatalogPollInterval
	}
	if f.Phase > PhaseOAuthInstallation || f.Reason > ReasonCatalogStale || f.Disposition > DispositionCancelled || f.Duration < 0 || f.Duration > contract.DiagnosticElapsedMaximum || f.Delay < 0 || f.Delay > maximumDelay {
		return false
	}
	if f.Event != UpstreamRetryScheduled && f.Retry != 0 {
		return false
	}
	if f.Event != UpstreamRetryScheduled && f.Event != CatalogPollScheduled && f.Delay != 0 {
		return false
	}
	if f.Event != UpstreamAttemptComplete && f.Event != OAuthRefreshComplete && f.Event != OAuthRefreshFailed && f.Duration != 0 {
		return false
	}
	switch f.Event {
	case UpstreamAttemptStart:
		return f.Attempt != 0 && f.Disposition == DispositionExecuting
	case UpstreamAttemptComplete:
		return f.Attempt != 0
	case UpstreamRetryScheduled:
		return f.Retry != 0 && f.Delay > 0 && f.Disposition == DispositionRetryScheduled
	case CatalogPollScheduled:
		return f.Phase == PhaseToolDiscovery && f.Delay > 0 && f.Disposition == DispositionRetryScheduled
	case UpstreamRetryReset:
		return f.Disposition == DispositionCancelled || f.Disposition == DispositionHealthy || f.Disposition == DispositionSuperseded || f.Disposition == DispositionStopped
	case UpstreamUnhealthy:
		return f.Disposition == DispositionRetryScheduled || f.Disposition == DispositionOperatorAuthentication || f.Disposition == DispositionStopped || f.Disposition == DispositionCleanupUncertain || f.Disposition == DispositionUnknown
	case UpstreamRecovered:
		return f.Disposition == DispositionHealthy && f.Reason == ReasonNone
	case OAuthRequired:
		return f.Phase == PhaseAuthorization && f.Disposition == DispositionOperatorAuthentication
	case OAuthCompleted:
		return f.Phase == PhaseAuthorization && f.Reason == ReasonNone
	case OAuthExpired:
		return f.Phase == PhaseAuthorization
	case OAuthFailed:
		return f.Phase == PhaseAuthorization || f.Phase >= PhaseOAuthMetadata
	case OAuthStage:
		return (f.Phase == PhaseAuthorization || f.Phase >= PhaseOAuthMetadata) && f.Reason == ReasonNone && f.Disposition == DispositionExecuting
	case OAuthRefreshComplete:
		return f.Phase == PhaseRefresh && f.Reason == ReasonNone
	case OAuthRefreshFailed:
		return f.Phase == PhaseRefresh
	default:
		return false
	}
}

type suppressionKey struct {
	upstream uint64
	event    Event
}
type suppressionState struct {
	phase       Phase
	reason      Reason
	disposition Disposition
	last        time.Time
	count       uint64
}

// Called only under the adapter's nonblocking producer lock. This owner never
// schedules lifecycle work or starts a timer; summaries occur on the next failure.
func (adapter *Adapter) suppress(f *Facts) bool {
	if f.Event == UpstreamRecovered {
		_, unhealthy := adapter.suppression[suppressionKey{f.Upstream, UpstreamUnhealthy}]
		_, refreshFailed := adapter.suppression[suppressionKey{f.Upstream, OAuthRefreshFailed}]
		delete(adapter.suppression, suppressionKey{f.Upstream, UpstreamUnhealthy})
		delete(adapter.suppression, suppressionKey{f.Upstream, OAuthRefreshFailed})
		return !unhealthy && !refreshFailed
	}
	if f.Event == OAuthRefreshComplete {
		delete(adapter.suppression, suppressionKey{f.Upstream, OAuthRefreshFailed})
		return false
	}
	if f.Event != UpstreamUnhealthy && f.Event != OAuthRefreshFailed {
		return false
	}
	// Uncorrelated observations cannot suppress another upstream's warning.
	if f.Upstream == 0 {
		return false
	}
	key := suppressionKey{f.Upstream, f.Event}
	now := adapter.now()
	prior, exists := adapter.suppression[key]
	if exists && prior.phase == f.Phase && prior.reason == f.Reason && prior.disposition == f.Disposition {
		if now.Sub(prior.last) < contract.DiagnosticSummaryInterval {
			if prior.count != ^uint64(0) {
				prior.count++
			}
			adapter.suppression[key] = prior
			return true
		}
		f.Suppressed = prior.count
	}
	if !exists && len(adapter.suppression) >= contract.DiagnosticSuppressionOwners {
		// Eviction loses noise history, never hides the new owner's first warning.
		var oldest suppressionKey
		var earliest time.Time
		for candidate, state := range adapter.suppression {
			if earliest.IsZero() || state.last.Before(earliest) {
				oldest, earliest = candidate, state.last
			}
		}
		delete(adapter.suppression, oldest)
	}
	adapter.suppression[key] = suppressionState{phase: f.Phase, reason: f.Reason, disposition: f.Disposition, last: now}
	return false
}
