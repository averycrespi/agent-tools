package contract

// HTTP policy dialect versions are immutable, unlike resource revisions.
const (
	HTTPPolicyVersion     = 1
	HTTPPolicyBytes       = 16384
	HTTPPolicyDepth       = 8
	HTTPPolicyGrants      = 4096
	HTTPPolicyOrigins     = 64
	HTTPPolicyCredentials = 256
	HTTPPolicyMethods     = 32
	HTTPMethodBytes       = 32
	HTTPHostBytes         = 253
	HTTPPathBytes         = 4096
	HTTPTargetBytes       = 8192
	HTTPAddressFacts      = 64
)

type HTTPGrantType string

const (
	HTTPBlockDestination HTTPGrantType = "block_destination"
	HTTPAllowTunnel      HTTPGrantType = "allow_tunnel"
	HTTPBlockRequests    HTTPGrantType = "block_requests"
	HTTPAllowRequests    HTTPGrantType = "allow_requests"
)

func HTTPGrantTypes() []HTTPGrantType {
	return []HTTPGrantType{HTTPBlockDestination, HTTPAllowTunnel, HTTPBlockRequests, HTTPAllowRequests}
}

type HTTPDefault string

const (
	HTTPDefaultBlock HTTPDefault = "block"
	HTTPDefaultAllow HTTPDefault = "allow"
)

type HTTPPathKind string

const (
	HTTPPathAny    HTTPPathKind = "any"
	HTTPPathExact  HTTPPathKind = "exact"
	HTTPPathPrefix HTTPPathKind = "segment_prefix"
)

// Hosts are exact unless spelled with the explicit leading *. operator.
// Ports are required effective ports; zero never means any/default port.
type HTTPDestinationSelector struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}
type HTTPOriginSelector struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   uint16 `json:"port"`
}
type HTTPMethods struct {
	Any    bool     `json:"any,omitempty"`
	Values []string `json:"values,omitempty"`
}
type HTTPPathSelector struct {
	Kind  HTTPPathKind `json:"kind"`
	Value string       `json:"value,omitempty"`
}
type HTTPRequestSelector struct {
	Origin  HTTPOriginSelector `json:"origin"`
	Methods HTTPMethods        `json:"methods"`
	Path    HTTPPathSelector   `json:"path"`
}

// HTTPPolicy is a closed tagged union, not an effect/scope combination.
// Only request allows accept CredentialID; only allows accept AllowPrivate.
// The parser rejects even explicitly false inapplicable options.
type HTTPPolicy struct {
	Version      int                      `json:"version"`
	Type         HTTPGrantType            `json:"type"`
	Destination  *HTTPDestinationSelector `json:"destination,omitempty"`
	Request      *HTTPRequestSelector     `json:"request,omitempty"`
	AllowPrivate *bool                    `json:"allow_private,omitempty"`
	CredentialID *string                  `json:"credential_id,omitempty"`
}

type HTTPTransport string

const (
	HTTPTransportNone      HTTPTransport = "none"
	HTTPTransportTunnel    HTTPTransport = "tunnel"
	HTTPTransportIntercept HTTPTransport = "intercept"
	HTTPTransportRequest   HTTPTransport = "request"
)

type HTTPDecisionReason string

const (
	HTTPReasonDestinationBlock      HTTPDecisionReason = "destination_block"
	HTTPReasonTunnelAllow           HTTPDecisionReason = "tunnel_allow"
	HTTPReasonIntercept             HTTPDecisionReason = "intercept_required"
	HTTPReasonRequestBlock          HTTPDecisionReason = "request_block"
	HTTPReasonRequestAllow          HTTPDecisionReason = "request_allow"
	HTTPReasonDefault               HTTPDecisionReason = "principal_default"
	HTTPReasonCredentialConflict    HTTPDecisionReason = "credential_conflict"    //nolint:gosec // Closed reason code, not credential material.
	HTTPReasonCredentialUnavailable HTTPDecisionReason = "credential_unavailable" //nolint:gosec // Closed reason code, not credential material.
	HTTPReasonAddressForbidden      HTTPDecisionReason = "address_forbidden"
	HTTPReasonPrivateRequired       HTTPDecisionReason = "private_permission_required"
)

type HTTPRevisionRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

// Evidence excludes URLs, paths, methods, headers, addresses and secrets.
// At most four grant refs (decisive, private, two credential sources) and two
// credential refs are retained, regardless of the number of matching grants.
type HTTPDecision struct {
	Version            int                `json:"version"`
	Principal          HTTPRevisionRef    `json:"principal"`
	PolicyRevision     uint64             `json:"policy_revision"`
	DefaultRevision    uint64             `json:"default_revision"`
	Transport          HTTPTransport      `json:"transport"`
	Allowed            bool               `json:"allowed"`
	Reason             HTTPDecisionReason `json:"reason"`
	Grant              *HTTPRevisionRef   `json:"grant,omitempty"`
	PrivateGrant       *HTTPRevisionRef   `json:"private_grant,omitempty"`
	Credential         *HTTPRevisionRef   `json:"credential,omitempty"`
	CredentialGrant    *HTTPRevisionRef   `json:"credential_grant,omitempty"`
	ConflictCredential *HTTPRevisionRef   `json:"conflict_credential,omitempty"`
	ConflictGrant      *HTTPRevisionRef   `json:"conflict_grant,omitempty"`
}
