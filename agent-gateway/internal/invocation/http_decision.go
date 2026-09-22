package invocation

import "github.com/averycrespi/agent-tools/agent-gateway/internal/contract"

// Validate the closed decision grammar independently of mutable current policy.
func validHTTPTrafficDecision(d contract.HTTPDecision, defaultPolicy contract.HTTPDefault, request bool) bool {
	if request != (d.Transport == contract.HTTPTransportRequest) || (d.Credential == nil) != (d.CredentialGrant == nil) || (d.ConflictCredential == nil) != (d.ConflictGrant == nil) {
		return false
	}
	if d.Credential != nil && (d.Transport != contract.HTTPTransportRequest || d.Grant == nil) {
		return false
	}
	if d.ConflictCredential != nil && (d.Credential == nil || d.Credential.ID == d.ConflictCredential.ID || d.CredentialGrant.ID >= d.ConflictGrant.ID) {
		return false
	}
	noExtras := d.PrivateGrant == nil && d.Credential == nil && d.ConflictCredential == nil
	switch d.Reason {
	case contract.HTTPReasonDestinationBlock:
		return !d.Allowed && d.Grant != nil && noExtras && (d.Transport == contract.HTTPTransportNone || d.Transport == contract.HTTPTransportRequest)
	case contract.HTTPReasonRequestBlock:
		return !d.Allowed && d.Grant != nil && noExtras && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonIntercept:
		return !d.Allowed && d.Grant == nil && noExtras && d.Transport == contract.HTTPTransportIntercept
	case contract.HTTPReasonTunnelAllow:
		return d.Allowed && d.Grant != nil && d.Credential == nil && d.ConflictCredential == nil && d.Transport == contract.HTTPTransportTunnel
	case contract.HTTPReasonRequestAllow:
		return d.Allowed && d.Grant != nil && d.ConflictCredential == nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonDefault:
		return d.Allowed == (defaultPolicy == contract.HTTPDefaultAllow) && d.Grant == nil && noExtras && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonCredentialConflict:
		return !d.Allowed && d.Grant != nil && d.ConflictCredential != nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonCredentialUnavailable:
		return !d.Allowed && d.Grant != nil && d.Credential != nil && d.ConflictCredential == nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonAddressForbidden, contract.HTTPReasonPrivateRequired:
		return !d.Allowed && d.ConflictCredential == nil && (d.Reason != contract.HTTPReasonPrivateRequired || d.PrivateGrant == nil) && ((d.Transport == contract.HTTPTransportTunnel && d.Grant != nil && d.Credential == nil) || (d.Transport == contract.HTTPTransportRequest && (d.Grant != nil || (defaultPolicy == contract.HTTPDefaultAllow && noExtras))))
	default:
		return false
	}
}
