package main

import (
	"net/netip"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"golang.org/x/net/idna"
)

// Validate public representation grammar only; the CLI never compiles policy,
// accesses private authority, evaluates a target, or resolves a destination.
func validHTTPGrant(g contract.HTTPGrant) bool {
	if !contract.ValidAuditID(g.ID) || !contract.ValidAuditID(g.PrincipalID) || !validCanonicalRevision(g.Revision) || g.Revision == "0" || g.Description != nil && !validGrantDescription(*g.Description) || g.State != contract.GrantActive && g.State != contract.GrantExpired {
		return false
	}
	created, ok := httpResponseTime(g.CreatedAt)
	if !ok {
		return false
	}
	updated, ok := httpResponseTime(g.UpdatedAt)
	if !ok || updated.Before(created) {
		return false
	}
	// Do not compare expiry with the client clock: it may pass in transit.
	if g.ExpiresAt == nil {
		if g.State == contract.GrantExpired {
			return false
		}
	} else {
		expiry, ok := httpResponseTime(*g.ExpiresAt)
		if !ok || !expiry.After(created) {
			return false
		}
	}
	var p contract.HTTPPolicy
	if len(g.Policy) > contract.HTTPPolicyBytes || controlclient.DecodeExactResponse(g.Policy, &p) != nil || p.Version != contract.HTTPPolicyVersion {
		return false
	}
	allow := p.Type == contract.HTTPAllowRequests || p.Type == contract.HTTPAllowTunnel
	if allow != (p.AllowPrivate != nil) || p.CredentialID != nil && (p.Type != contract.HTTPAllowRequests || !contract.ValidAuditID(*p.CredentialID)) {
		return false
	}
	switch p.Type {
	case contract.HTTPBlockDestination, contract.HTTPAllowTunnel:
		return p.Request == nil && p.Destination != nil && p.Destination.Port > 0 && validHTTPResponseHost(p.Destination.Host)
	case contract.HTTPBlockRequests, contract.HTTPAllowRequests:
		if p.Destination != nil || p.Request == nil {
			return false
		}
		r := p.Request
		if r.Origin.Port == 0 || !validHTTPResponseHost(r.Origin.Host) || r.Origin.Scheme != "http" && r.Origin.Scheme != "https" || p.CredentialID != nil && r.Origin.Scheme != "https" {
			return false
		}
		if r.Methods.Any {
			if r.Methods.Values != nil {
				return false
			}
		} else {
			if len(r.Methods.Values) == 0 || len(r.Methods.Values) > contract.HTTPPolicyMethods {
				return false
			}
			for i, m := range r.Methods.Values {
				if !validHTTPResponseMethod(m) || i > 0 && r.Methods.Values[i-1] >= m {
					return false
				}
			}
		}
		switch r.Path.Kind {
		case contract.HTTPPathAny:
			return r.Path.Value == ""
		case contract.HTTPPathExact:
			return validHTTPResponsePath(r.Path.Value)
		case contract.HTTPPathPrefix:
			return validHTTPResponsePath(r.Path.Value) && (r.Path.Value == "/" || !strings.HasSuffix(r.Path.Value, "/"))
		default:
			return false
		}
	default:
		return false
	}
}

func httpResponseTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	return t, err == nil && t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") == value
}
func validHTTPResponseHost(value string) bool {
	wildcard := strings.HasPrefix(value, "*.")
	host := strings.TrimPrefix(value, "*.")
	if len(host) == 0 || len(host) > contract.HTTPHostBytes {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return !wildcard && ip.Zone() == "" && ip.Unmap().String() == host
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii != host {
		return false
	}
	normalized, ok := contract.NormalizeHostname(host)
	if !ok || normalized != host || wildcard && !strings.Contains(host, ".") {
		return false
	}
	last := host[strings.LastIndex(host, ".")+1:]
	return !strings.HasPrefix(last, "0x") || strings.Trim(last[2:], "0123456789abcdef") != ""
}
func validHTTPResponseMethod(value string) bool {
	if len(value) == 0 || len(value) > contract.HTTPMethodBytes || value == "CONNECT" {
		return false
	}
	for _, b := range []byte(value) {
		if (b < 'A' || b > 'Z') && (b < '0' || b > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			return false
		}
	}
	return true
}
func validHTTPResponsePath(value string) bool {
	if len(value) == 0 || len(value) > contract.HTTPPathBytes || value[0] != '/' || strings.Contains(value, "//") {
		return false
	}
	for _, b := range []byte(value) {
		if (b < 'A' || b > 'Z') && (b < 'a' || b > 'z') && (b < '0' || b > '9') && !strings.ContainsRune("/-._~", rune(b)) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
func validHTTPPreview(p contract.HTTPAccessPreview) bool {
	d := p.Decision
	validRef := func(r *contract.HTTPRevisionRef) bool {
		return r != nil && contract.ValidAuditID(r.ID) && r.Revision > 0
	}
	if !p.PolicyOnly || p.NetworkVerified || p.TLSVerified || p.MaterialVerified || p.AdmissionAuthority || p.Default != contract.HTTPDefaultAllow && p.Default != contract.HTTPDefaultBlock || d.Version != contract.HTTPPolicyVersion || !validRef(&d.Principal) || d.PolicyRevision == 0 || d.DefaultRevision == 0 {
		return false
	}
	for _, ref := range []*contract.HTTPRevisionRef{d.Grant, d.PrivateGrant, d.Credential, d.CredentialGrant, d.ConflictCredential, d.ConflictGrant} {
		if ref != nil && !validRef(ref) {
			return false
		}
	}
	if (d.Credential == nil) != (d.CredentialGrant == nil) || (d.ConflictCredential == nil) != (d.ConflictGrant == nil) {
		return false
	}
	if d.ConflictCredential != nil && (d.Credential == nil || d.Credential.ID == d.ConflictCredential.ID || d.CredentialGrant.ID >= d.ConflictGrant.ID) {
		return false
	}
	noExtras := d.PrivateGrant == nil && d.Credential == nil && d.ConflictCredential == nil
	if d.Credential != nil && (d.Transport != contract.HTTPTransportRequest || d.Grant == nil) {
		return false
	}
	switch d.Reason {
	case contract.HTTPReasonDestinationBlock:
		return !d.Allowed && d.Grant != nil && noExtras && (d.Transport == contract.HTTPTransportNone || d.Transport == contract.HTTPTransportRequest)
	case contract.HTTPReasonRequestBlock:
		return !d.Allowed && d.Grant != nil && noExtras && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonIntercept:
		return !d.Allowed && d.Grant == nil && noExtras && d.Transport == contract.HTTPTransportIntercept
	case contract.HTTPReasonTunnelAllow:
		return d.Allowed && d.Grant != nil && d.Credential == nil && d.Transport == contract.HTTPTransportTunnel
	case contract.HTTPReasonRequestAllow:
		return d.Allowed && d.Grant != nil && d.ConflictCredential == nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonDefault:
		return d.Allowed == (p.Default == contract.HTTPDefaultAllow) && d.Grant == nil && noExtras && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonCredentialConflict:
		return !d.Allowed && d.Grant != nil && d.ConflictCredential != nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonCredentialUnavailable:
		return !d.Allowed && d.Grant != nil && d.Credential != nil && d.ConflictCredential == nil && d.Transport == contract.HTTPTransportRequest
	case contract.HTTPReasonAddressForbidden, contract.HTTPReasonPrivateRequired:
		if d.Allowed || d.ConflictCredential != nil || d.Reason == contract.HTTPReasonPrivateRequired && d.PrivateGrant != nil {
			return false
		}
		if d.Transport == contract.HTTPTransportTunnel {
			return d.Grant != nil && d.Credential == nil
		}
		return d.Transport == contract.HTTPTransportRequest && (d.Grant != nil || p.Default == contract.HTTPDefaultAllow && noExtras)
	default:
		return false
	}
}
