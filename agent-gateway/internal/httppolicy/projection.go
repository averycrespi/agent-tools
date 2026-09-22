package httppolicy

import (
	"encoding/json"
	"slices"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// JSON returns canonical configuration, never observed traffic coordinates.
func (p Policy) JSON() ([]byte, error) {
	if !p.valid {
		return nil, ErrInvalid
	}
	host := p.host.host
	if p.host.wildcard {
		host = "*." + host
	}
	out := contract.HTTPPolicy{Version: contract.HTTPPolicyVersion, Type: p.kind}
	switch p.kind {
	case contract.HTTPBlockDestination, contract.HTTPAllowTunnel:
		out.Destination = &contract.HTTPDestinationSelector{Host: host, Port: p.port}
	default:
		out.Request = &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: p.scheme, Host: host, Port: p.port}, Methods: contract.HTTPMethods{Any: len(p.methods) == 0, Values: slices.Clone(p.methods)}, Path: contract.HTTPPathSelector{Kind: p.pathKind, Value: p.path}}
	}
	if p.kind == contract.HTTPAllowRequests || p.kind == contract.HTTPAllowTunnel {
		private := p.private
		out.AllowPrivate = &private
	}
	if p.credential != "" {
		id := p.credential
		out.CredentialID = &id
	}
	return json.Marshal(out)
}
