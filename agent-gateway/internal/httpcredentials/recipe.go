// Package httpcredentials owns scoped HTTP credential metadata and material.
package httpcredentials

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

var ErrInvalid = errors.New("invalid HTTP credential")

// Boundary is HTTPS-only. Wildcards require both the *. spelling and explicit
// opt-in; the hostname grammar and containment remain owned by httppolicy.
type Boundary struct {
	Host          string `json:"host"`
	Port          uint16 `json:"port"`
	AllowWildcard bool   `json:"allow_wildcard"`
}

type Definition struct {
	Name     string                        `json:"name"`
	Boundary Boundary                      `json:"boundary"`
	Recipe   contract.HTTPCredentialRecipe `json:"recipe"`
}

func Normalize(def Definition) (Definition, error) {
	if def.Name == "" || len(def.Name) > contract.HTTPCredentialNameBytes || !utf8.ValidString(def.Name) || strings.TrimSpace(def.Name) != def.Name || !contract.ValidHTTPCredentialRecipe(def.Recipe) {
		return Definition{}, ErrInvalid
	}
	for _, r := range def.Name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return Definition{}, ErrInvalid
		}
	}
	host := def.Boundary.Host
	wildcard := strings.HasPrefix(host, "*.")
	if wildcard != def.Boundary.AllowWildcard {
		return Definition{}, ErrInvalid
	}
	if wildcard {
		host = host[2:]
	}
	destination, err := httppolicy.NewDestination(host, def.Boundary.Port)
	if err != nil {
		return Definition{}, ErrInvalid
	}
	def.Boundary.Host = destination.Host()
	if wildcard {
		def.Boundary.Host = "*." + def.Boundary.Host
	}
	// Compile through the policy owner to reject wildcard IPs and invalid host
	// scopes without maintaining another parser here.
	_, err = httppolicy.Compile(contract.HTTPPolicy{Version: contract.HTTPPolicyVersion, Type: contract.HTTPAllowRequests, Request: &contract.HTTPRequestSelector{
		Origin: def.Boundary.origin(), Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny},
	}})
	if err != nil {
		return Definition{}, ErrInvalid
	}
	def.Recipe.Header = http.CanonicalHeaderKey(def.Recipe.Header)
	return def, nil
}

func (b Boundary) origin() contract.HTTPOriginSelector {
	return contract.HTTPOriginSelector{Scheme: "https", Host: b.Host, Port: b.Port}
}

func (d Definition) PolicyCredential(ref contract.HTTPRevisionRef, available bool) httppolicy.Credential {
	return httppolicy.Credential{Ref: ref, Available: available, Origins: []contract.HTTPOriginSelector{d.Boundary.origin()}}
}

// Material is a request-local pinned generation. It has no exported secret or
// JSON fields. It must not be persisted or reused after the owning admission.
type Material struct {
	ref        contract.HTTPRevisionRef
	definition Definition
	value      []byte
	generation string
}

func newMaterial(ref contract.HTTPRevisionRef, def Definition, secret []byte) (*Material, error) {
	defer clear(secret)
	canonical, err := Normalize(def)
	if err != nil || !contract.ValidAuditID(ref.ID) || ref.Revision == 0 || !contract.ValidHTTPCredentialSecret(canonical.Recipe, secret) {
		return nil, ErrInvalid
	}
	value := make([]byte, 0, len(canonical.Recipe.Prefix)+len(secret))
	value = append(value, canonical.Recipe.Prefix...)
	value = append(value, secret...)
	return &Material{ref: ref, definition: canonical, value: value}, nil
}

func (*Material) String() string                        { return "HTTP credential material [redacted]" }
func (m *Material) GoString() string                    { return m.String() }
func (m *Material) Reference() contract.HTTPRevisionRef { return m.ref }
func (m *Material) Clear()                              { clear(m.value); m.value = nil }

// Headers resolves and validates all required material before returning a fresh
// header map. Every case variant is replaced, never appended. Failure leaves
// the input untouched. The forwarding owner must use the supplied canonical
// request and this returned map, not reparse a different destination afterward.
func (m *Material) Headers(target httppolicy.Request, original http.Header) (http.Header, error) {
	if m == nil || len(m.value) == 0 {
		return nil, ErrInvalid
	}
	allowed, err := httppolicy.CredentialAllowsRequest(m.definition.PolicyCredential(m.ref, true), target)
	if err != nil || !allowed {
		return nil, ErrInvalid
	}
	// Connection-nominated fields are hop-by-hop regardless of their spelling.
	for name, values := range original {
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					if strings.EqualFold(strings.TrimSpace(token), m.definition.Recipe.Header) {
						return nil, ErrInvalid
					}
				}
			}
		}
	}
	out := original.Clone()
	if out == nil {
		out = make(http.Header)
	}
	for name := range out {
		if strings.EqualFold(name, m.definition.Recipe.Header) {
			delete(out, name)
		}
	}
	out[m.definition.Recipe.Header] = []string{string(m.value)}
	return out, nil
}
