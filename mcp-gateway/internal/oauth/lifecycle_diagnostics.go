package oauth

import (
	"context"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

// SetDiagnostics is composition-time only. Resource identity is used only by
// the private reference resolver and is never passed to the typed observer.
func (service *FlowService) SetDiagnostics(observer diagnostics.ReconciliationObserver, reference func(string) uint64) {
	service.diagnostics, service.diagnosticReference = observer, reference
}
func (service *RefreshService) SetDiagnostics(observer diagnostics.ReconciliationObserver, reference func(string) uint64) {
	service.diagnostics, service.diagnosticReference = observer, reference
}
func (service *FlowService) observeFlow(serverID string, attempt uint64, event diagnostics.Event, phase diagnostics.Phase, reason diagnostics.Reason) {
	if service.diagnostics == nil {
		return
	}
	facts := diagnostics.Facts{Event: event, Attempt: attempt, Phase: phase, Reason: reason, Disposition: diagnostics.DispositionUnknown}
	if event == diagnostics.OAuthRequired || event == diagnostics.OAuthExpired {
		facts.Disposition = diagnostics.DispositionOperatorAuthentication
	}
	if event == diagnostics.OAuthStage {
		facts.Disposition = diagnostics.DispositionExecuting
	}
	if service.diagnosticReference != nil {
		facts.Upstream = service.diagnosticReference(serverID)
	}
	service.diagnostics.Reconciliation(facts)
}
func oauthPhase(stage contract.OAuthDiagnosticStage) diagnostics.Phase {
	switch stage {
	case contract.OAuthDiagnosticMetadataDiscovery:
		return diagnostics.PhaseOAuthMetadata
	case contract.OAuthDiagnosticClientRegistration:
		return diagnostics.PhaseOAuthRegistration
	case contract.OAuthDiagnosticCallbackValidation:
		return diagnostics.PhaseOAuthCallback
	case contract.OAuthDiagnosticTokenExchange:
		return diagnostics.PhaseOAuthExchange
	case contract.OAuthDiagnosticCredentialInstallation:
		return diagnostics.PhaseOAuthInstallation
	default:
		return diagnostics.PhaseAuthorization
	}
}

func (service *RefreshService) observeRefresh(serverID string, start time.Time, result RefreshResult, err error) {
	if service.diagnostics == nil {
		return
	}
	if errors.Is(err, ErrRefreshIneligible) || errors.Is(err, servers.ErrStaleRevision) || errors.Is(err, keyring.ErrDraining) || errors.Is(err, context.Canceled) {
		return
	}
	facts := diagnostics.Facts{Phase: diagnostics.PhaseRefresh, Duration: diagnostics.Elapsed(start, service.now()), Disposition: diagnostics.DispositionUnknown}
	if err == nil {
		if !result.Refreshed {
			return
		}
		facts.Event, facts.Reason = diagnostics.OAuthRefreshComplete, diagnostics.ReasonNone
	} else {
		facts.Event, facts.Reason = diagnostics.OAuthRefreshFailed, diagnostics.ReasonUnknown
		switch {
		case errors.Is(err, ErrRefreshReauthorization):
			facts.Reason, facts.Disposition = diagnostics.ReasonAuthenticationRequired, diagnostics.DispositionOperatorAuthentication
		case errors.Is(err, context.DeadlineExceeded):
			facts.Reason = diagnostics.ReasonTimeout
		case errors.Is(err, keyring.ErrWorkLimit), errors.Is(err, keyring.ErrCandidateLimit):
			facts.Reason = diagnostics.ReasonResourceLimit
		case errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrNoAuthority):
			facts.Reason = diagnostics.ReasonCredentialAbsent
		default:
			var capability *keyring.CapabilityError
			if errors.As(err, &capability) {
				switch capability.Capability.State {
				case contract.KeyringLocked:
					facts.Reason = diagnostics.ReasonKeyringLocked
				case contract.KeyringInteractionRequired:
					facts.Reason = diagnostics.ReasonKeyringInteractionRequired
				default:
					facts.Reason = diagnostics.ReasonKeyringUnavailable
				}
			}
		}
	}
	if service.diagnosticReference != nil {
		facts.Upstream = service.diagnosticReference(serverID)
	}
	service.diagnostics.Reconciliation(facts)
}
