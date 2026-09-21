package httppolicy

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

type hostScope struct {
	host     string
	wildcard bool
}

func parseHostScope(raw string) (hostScope, error) {
	wildcard := strings.HasPrefix(raw, "*.")
	if wildcard {
		raw = raw[2:]
	}
	host, err := canonicalHost(raw)
	if err != nil || wildcard && (strings.Contains(host, ":") || !strings.Contains(host, ".") || strings.Trim(host, "0123456789.") == "") {
		return hostScope{}, ErrInvalid
	}
	return hostScope{host, wildcard}, nil
}
func (s hostScope) matches(host string) bool {
	return !s.wildcard && host == s.host || s.wildcard && strings.HasSuffix(host, "."+s.host)
}
func (s hostScope) covers(other hostScope) bool {
	if !other.wildcard {
		return s.matches(other.host)
	}
	return s.wildcard && (s.host == other.host || strings.HasSuffix(other.host, "."+s.host))
}

type originScope struct {
	host   hostScope
	scheme string
	port   uint16
}

func parseOrigin(s contract.HTTPOriginSelector) (originScope, error) {
	h, err := parseHostScope(s.Host)
	if err != nil || s.Port == 0 || s.Scheme != "http" && s.Scheme != "https" {
		return originScope{}, ErrInvalid
	}
	return originScope{h, s.Scheme, s.Port}, nil
}
func (s originScope) covers(other originScope) bool {
	return s.scheme == other.scheme && s.port == other.port && s.host.covers(other.host)
}

// Policy is immutable after compilation. Copies share only unexported immutable
// slices. Version dispatch must never reinterpret these v1 semantics.
type Policy struct {
	valid      bool
	kind       contract.HTTPGrantType
	host       hostScope
	port       uint16
	scheme     string
	methods    []string
	pathKind   contract.HTTPPathKind
	path       string
	private    bool
	credential string
}

// DecodePolicy rejects duplicate/unknown/case-aliased fields, nulls, trailing
// values and unsupported dialects before compiling the closed union.
func DecodePolicy(raw []byte) (Policy, error) {
	v, err := strictjson.ParseValue(raw, strictjson.Options{MaxBytes: contract.HTTPPolicyBytes, MaxDepth: contract.HTTPPolicyDepth})
	if err != nil || !policyShape(v, "policy") {
		return Policy{}, ErrInvalid
	}
	var p contract.HTTPPolicy
	if err = json.Unmarshal(raw, &p); err != nil {
		return Policy{}, ErrInvalid
	}
	return Compile(p)
}
func policyShape(v strictjson.Value, shape string) bool {
	var fields map[string]string
	switch shape {
	case "policy":
		fields = map[string]string{"version": "number", "type": "string", "destination": "destination", "request": "request", "allow_private": "boolean", "credential_id": "string"}
	case "destination":
		fields = map[string]string{"host": "string", "port": "number"}
	case "origin":
		fields = map[string]string{"scheme": "string", "host": "string", "port": "number"}
	case "request":
		fields = map[string]string{"origin": "origin", "methods": "methods", "path": "path"}
	case "methods":
		if v.Type != strictjson.ValueObject || len(v.Object) != 1 {
			return false
		}
		member := v.Object[0]
		if member.Name == "any" {
			return member.Value.Type == strictjson.ValueBoolean && member.Value.Boolean
		}
		return member.Name == "values" && len(member.Value.Array) > 0 && policyShape(member.Value, "strings")
	case "path":
		fields = map[string]string{"kind": "string", "value": "string"}
		for _, member := range v.Object {
			if member.Name == "kind" && member.Value.String == string(contract.HTTPPathAny) && len(v.Object) != 1 {
				return false
			}
		}
	case "strings":
		if v.Type != strictjson.ValueArray {
			return false
		}
		for _, x := range v.Array {
			if x.Type != strictjson.ValueString {
				return false
			}
		}
		return true
	default:
		return string(v.Type) == shape
	}
	if v.Type != strictjson.ValueObject {
		return false
	}
	for _, m := range v.Object {
		s, ok := fields[m.Name]
		if !ok || !policyShape(m.Value, s) {
			return false
		}
	}
	return true
}

func Compile(p contract.HTTPPolicy) (Policy, error) {
	if p.Version != contract.HTTPPolicyVersion {
		return Policy{}, ErrInvalid
	}
	allow := p.Type == contract.HTTPAllowTunnel || p.Type == contract.HTTPAllowRequests
	if !allow && p.AllowPrivate != nil || p.Type != contract.HTTPAllowRequests && p.CredentialID != nil {
		return Policy{}, ErrInvalid
	}
	c := Policy{valid: true, kind: p.Type}
	if p.AllowPrivate != nil {
		c.private = *p.AllowPrivate
	}
	if p.CredentialID != nil {
		if !validID(*p.CredentialID) {
			return Policy{}, ErrInvalid
		}
		c.credential = *p.CredentialID
	}
	switch p.Type {
	case contract.HTTPBlockDestination, contract.HTTPAllowTunnel:
		if p.Destination == nil || p.Request != nil || p.Destination.Port == 0 {
			return Policy{}, ErrInvalid
		}
		h, err := parseHostScope(p.Destination.Host)
		if err != nil {
			return Policy{}, err
		}
		c.host = h
		c.port = p.Destination.Port
	case contract.HTTPBlockRequests, contract.HTTPAllowRequests:
		if p.Request == nil || p.Destination != nil {
			return Policy{}, ErrInvalid
		}
		r := p.Request
		origin, err := parseOrigin(r.Origin)
		if err != nil {
			return Policy{}, err
		}
		if c.credential != "" && origin.scheme != "https" {
			return Policy{}, ErrInvalid
		}
		c.host = origin.host
		c.port = origin.port
		c.scheme = origin.scheme
		if r.Methods.Any {
			if r.Methods.Values != nil {
				return Policy{}, ErrInvalid
			}
		} else {
			if len(r.Methods.Values) == 0 || len(r.Methods.Values) > contract.HTTPPolicyMethods {
				return Policy{}, ErrInvalid
			}
			c.methods = append([]string(nil), r.Methods.Values...)
			slices.Sort(c.methods)
			for i, m := range c.methods {
				if !validMethod(m) || m == "CONNECT" || i > 0 && m == c.methods[i-1] {
					return Policy{}, ErrInvalid
				}
			}
		}
		c.pathKind = r.Path.Kind
		switch r.Path.Kind {
		case contract.HTTPPathAny:
			if r.Path.Value != "" {
				return Policy{}, ErrInvalid
			}
		case contract.HTTPPathExact, contract.HTTPPathPrefix:
			if r.Path.Value == "" {
				return Policy{}, ErrInvalid
			}
			c.path, err = canonicalPath(r.Path.Value)
			if err != nil {
				return Policy{}, err
			}
			if c.pathKind == contract.HTTPPathPrefix && c.path != "/" {
				c.path = strings.TrimSuffix(c.path, "/")
			}
		default:
			return Policy{}, ErrInvalid
		}
	default:
		return Policy{}, ErrInvalid
	}
	return c, nil
}
func (p Policy) matchesDestination(d Destination) bool {
	return d.port == p.port && p.host.matches(d.host)
}
func (p Policy) matchesRequest(r Request) bool {
	if !p.matchesDestination(r.destination) || p.scheme != r.scheme || len(p.methods) > 0 && !slices.Contains(p.methods, r.method) {
		return false
	}
	switch p.pathKind {
	case contract.HTTPPathAny:
		return true
	case contract.HTTPPathExact:
		return p.path == r.path
	case contract.HTTPPathPrefix:
		return p.path == "/" || p.path == r.path || strings.HasPrefix(r.path, p.path+"/")
	default:
		return false
	}
}

// Credential contains no secret. Available is a coherent owner-supplied fact;
// later acquisition failure must reject, never retry uninjected.
type Credential struct {
	Ref       contract.HTTPRevisionRef
	Available bool
	Origins   []contract.HTTPOriginSelector
}
type compiledCredential struct {
	ref       contract.HTTPRevisionRef
	available bool
	origins   []originScope
}

func compileCredential(c Credential) (compiledCredential, error) {
	if !validRef(c.Ref) || len(c.Origins) == 0 || len(c.Origins) > contract.HTTPPolicyOrigins {
		return compiledCredential{}, ErrInvalid
	}
	out := compiledCredential{ref: c.Ref, available: c.Available}
	for _, o := range c.Origins {
		origin, err := parseOrigin(o)
		if err != nil || origin.scheme != "https" {
			return compiledCredential{}, ErrInvalid
		}
		out.origins = append(out.origins, origin)
	}
	return out, nil
}
func (c compiledCredential) covers(p Policy) bool {
	origin := originScope{p.host, p.scheme, p.port}
	for _, boundary := range c.origins {
		if boundary.covers(origin) {
			return true
		}
	}
	return false
}

// CredentialContains proves full set containment, not intersection. With one
// exact/wildcard host and one port per origin, a finite union covers a wildcard
// only when some member covers that entire subtree.
func CredentialContains(c Credential, p Policy) (bool, error) {
	boundary, err := compileCredential(c)
	if err != nil || !p.valid || p.kind != contract.HTTPAllowRequests || p.scheme != "https" {
		return false, ErrInvalid
	}
	return boundary.covers(p), nil
}
func validID(s string) bool                    { return contract.ValidAuditID(s) }
func validRef(r contract.HTTPRevisionRef) bool { return validID(r.ID) && r.Revision > 0 }
