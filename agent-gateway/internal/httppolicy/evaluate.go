package httppolicy

import (
	"slices"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// Snapshot is a coherent authority-owner input, not an authenticator or lease.
// Principal state/credential admission and expiry filtering belong to that owner.
type Snapshot struct {
	Principal       contract.HTTPRevisionRef
	PolicyRevision  uint64
	DefaultRevision uint64
	Default         contract.HTTPDefault
	Grants          []Grant
	Credentials     []Credential
}
type Grant struct {
	Ref         contract.HTTPRevisionRef
	PrincipalID string
	Policy      Policy
}
type Evaluator struct {
	principal                 contract.HTTPRevisionRef
	revision, defaultRevision uint64
	defaultPolicy             contract.HTTPDefault
	grants                    []Grant
	credentials               map[string]compiledCredential
}

// New copies and validates the entire bounded snapshot, including grants that
// do not match this target/principal. Malformed authority never yields a partial
// allow. Input ordering affects neither authority nor explanatory references.
func New(s Snapshot) (*Evaluator, error) {
	if !validRef(s.Principal) || s.PolicyRevision == 0 || s.DefaultRevision == 0 || s.Default != contract.HTTPDefaultAllow && s.Default != contract.HTTPDefaultBlock || len(s.Grants) > contract.HTTPPolicyGrants || len(s.Credentials) > contract.HTTPPolicyCredentials {
		return nil, ErrInvalid
	}
	e := &Evaluator{principal: s.Principal, revision: s.PolicyRevision, defaultRevision: s.DefaultRevision, defaultPolicy: s.Default, credentials: make(map[string]compiledCredential)}
	for _, c := range s.Credentials {
		cc, err := compileCredential(c)
		if err != nil {
			return nil, err
		}
		if _, exists := e.credentials[c.Ref.ID]; exists {
			return nil, ErrInvalid
		}
		e.credentials[c.Ref.ID] = cc
	}
	seen := make(map[string]bool)
	for _, g := range s.Grants {
		if !validRef(g.Ref) || !validID(g.PrincipalID) || !g.Policy.valid || seen[g.Ref.ID] {
			return nil, ErrInvalid
		}
		seen[g.Ref.ID] = true
		if g.Policy.credential != "" {
			c, exists := e.credentials[g.Policy.credential]
			if !exists || !c.covers(g.Policy) {
				return nil, ErrInvalid
			}
		}
		if g.PrincipalID == s.Principal.ID {
			e.grants = append(e.grants, g)
		}
	}
	slices.SortFunc(e.grants, func(a, b Grant) int { return strings.Compare(a.Ref.ID, b.Ref.ID) })
	return e, nil
}
func (e *Evaluator) decision(t contract.HTTPTransport) contract.HTTPDecision {
	return contract.HTTPDecision{Version: contract.HTTPPolicyVersion, Principal: e.principal, PolicyRevision: e.revision, DefaultRevision: e.defaultRevision, Transport: t}
}
func (e *Evaluator) destinationBlock(d Destination) *contract.HTTPRevisionRef {
	for _, g := range e.grants {
		if g.Policy.kind == contract.HTTPBlockDestination && g.Policy.matchesDestination(d) {
			ref := g.Ref
			return &ref
		}
	}
	return nil
}

// Connect selects an opaque tunnel or local interception. Intercept is not an
// upstream allow: evaluate each decrypted request before connecting/forwarding.
// Request grants and credentials never compete with a matching tunnel grant.
func (e *Evaluator) Connect(d Destination, facts AddressFacts) (contract.HTTPDecision, error) {
	if e == nil || d.host == "" || d.port == 0 {
		return contract.HTTPDecision{}, ErrInvalid
	}
	result := e.decision(contract.HTTPTransportNone)
	if ref := e.destinationBlock(d); ref != nil {
		result.Grant = ref
		result.Reason = contract.HTTPReasonDestinationBlock
		return result, nil
	}
	for _, g := range e.grants {
		if g.Policy.kind != contract.HTTPAllowTunnel || !g.Policy.matchesDestination(d) {
			continue
		}
		if result.Grant == nil {
			ref := g.Ref
			result.Grant = &ref
		}
		if g.Policy.private && result.PrivateGrant == nil {
			ref := g.Ref
			result.PrivateGrant = &ref
		}
	}
	if result.Grant == nil {
		result.Transport = contract.HTTPTransportIntercept
		result.Reason = contract.HTTPReasonIntercept
		return result, nil
	}
	result.Transport = contract.HTTPTransportTunnel
	result.Reason = contract.HTTPReasonTunnelAllow
	return finishAddress(result, d, facts)
}

func (e *Evaluator) Request(r Request, facts AddressFacts) (contract.HTTPDecision, error) {
	if e == nil || r.destination.host == "" || r.path == "" {
		return contract.HTTPDecision{}, ErrInvalid
	}
	result := e.decision(contract.HTTPTransportRequest)
	if ref := e.destinationBlock(r.destination); ref != nil {
		result.Grant = ref
		result.Reason = contract.HTTPReasonDestinationBlock
		return result, nil
	}
	for _, g := range e.grants {
		if g.Policy.kind == contract.HTTPBlockRequests && g.Policy.matchesRequest(r) {
			ref := g.Ref
			result.Grant = &ref
			result.Reason = contract.HTTPReasonRequestBlock
			return result, nil
		}
	}
	for _, g := range e.grants {
		p := g.Policy
		if p.kind != contract.HTTPAllowRequests || !p.matchesRequest(r) {
			continue
		}
		if result.Grant == nil {
			ref := g.Ref
			result.Grant = &ref
		}
		if p.private && result.PrivateGrant == nil {
			ref := g.Ref
			result.PrivateGrant = &ref
		}
		if p.credential != "" {
			e.selectCredential(&result, g)
		}
	}
	if result.ConflictCredential != nil {
		result.Reason = contract.HTTPReasonCredentialConflict
		return result, nil
	}
	if result.Credential != nil && !e.credentials[result.Credential.ID].available {
		result.Reason = contract.HTTPReasonCredentialUnavailable
		return result, nil
	}
	result.Reason = contract.HTTPReasonRequestAllow
	if result.Grant == nil {
		result.Reason = contract.HTTPReasonDefault
		if e.defaultPolicy != contract.HTTPDefaultAllow {
			return result, nil
		}
	}
	return finishAddress(result, r.destination, facts)
}
func (e *Evaluator) selectCredential(result *contract.HTTPDecision, g Grant) {
	ref := e.credentials[g.Policy.credential].ref
	// Grant traversal is sorted by ID solely to select stable evidence, never
	// as a priority rule. Retain the first two distinct credential requirements.
	if result.Credential == nil {
		result.Credential = &ref
		source := g.Ref
		result.CredentialGrant = &source
	} else if result.Credential.ID != ref.ID && result.ConflictCredential == nil {
		result.ConflictCredential = &ref
		source := g.Ref
		result.ConflictGrant = &source
	}
}
func finishAddress(result contract.HTTPDecision, d Destination, facts AddressFacts) (contract.HTTPDecision, error) {
	reason, err := checkAddresses(d, facts, result.PrivateGrant != nil)
	if err != nil {
		return contract.HTTPDecision{}, err
	}
	if reason != "" {
		result.Reason = reason
		return result, nil
	}
	result.Allowed = true
	return result, nil
}
