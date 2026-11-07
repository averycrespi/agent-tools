package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"
)

var configNamespace = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidateConfigExport checks the versioned graph, not secret availability.
// Domain owners additionally validate their configuration before serialization.
func ValidateConfigExport(v ConfigExport) error {
	invalid := fmt.Errorf("invalid portable configuration document")
	if v.Format != ConfigExportFormat || v.Version != ConfigExportVersion || v.Agents == nil || v.MCP.Servers == nil || v.MCP.Grants == nil || v.HTTP.Credentials == nil || v.HTTP.Grants == nil || v.Git.Credentials == nil || v.Git.Repositories == nil || v.Git.Grants == nil || v.Git.RoutingOrigins == nil {
		return invalid
	}
	if len(v.Agents) > 128 || len(v.MCP.Servers) > 1024 || len(v.MCP.Grants) > 4096 || len(v.HTTP.Credentials) > 256 || len(v.HTTP.Grants) > HTTPPolicyGrants || len(v.Git.Credentials) > GitCredentials || len(v.Git.Repositories) > GitRepositoryIdentities || len(v.Git.Grants) > GitGrants {
		return invalid
	}
	agents := map[string]bool{}
	servers := map[string]bool{"mcp/" + SyntheticServerNamespace: true}
	httpCredentials := map[string]bool{}
	gitCredentials := map[string]bool{}
	repositories := map[string]bool{}
	for i, a := range v.Agents {
		if a.Ref != fmt.Sprintf("agent/%d", i+1) || agents[a.Ref] || a.DisplayName == "" {
			return invalid
		}
		if _, err := ParsePrincipalState(string(a.State)); err != nil {
			return invalid
		}
		if _, err := ParsePrincipalVisibility(string(a.Visibility)); err != nil {
			return invalid
		}
		if a.HTTPDefault != HTTPDefaultBlock && a.HTTPDefault != HTTPDefaultAllow {
			return invalid
		}
		agents[a.Ref] = true
	}
	for _, s := range v.MCP.Servers {
		if !configNamespace.MatchString(s.Namespace) || s.Namespace == SyntheticServerNamespace || s.Ref != "mcp/"+s.Namespace || servers[s.Ref] || s.DisplayName == "" || s.Credentials.Static == nil {
			return invalid
		}
		if s.Deleted && (s.Enabled || !bytes.Equal(s.Transport, []byte("null")) || len(s.Credentials.Static) > 0 || s.Credentials.OAuthClientSecret != nil || s.Credentials.OAuthAuthorization) {
			return invalid
		}
		if !s.Deleted && (!json.Valid(s.Transport) || len(s.Transport) == 0 || s.Transport[0] != '{') {
			return invalid
		}
		for slot, ref := range s.Credentials.Static {
			if slot == "" || ref != s.Ref+"/static/"+slot {
				return invalid
			}
		}
		if s.Credentials.OAuthClientSecret != nil && (*s.Credentials.OAuthClientSecret != s.Ref+"/oauth/client_secret" || !s.Credentials.OAuthAuthorization) {
			return invalid
		}
		if !s.Deleted && !ValidConfigServerCredentials(s) {
			return invalid
		}
		servers[s.Ref] = true
	}
	for i, c := range v.HTTP.Credentials {
		if c.Ref != fmt.Sprintf("http-credential/%d", i+1) || c.SecretRef != c.Ref+"/secret" || c.Name == "" || c.Boundary.Host == "" || c.Boundary.Port == 0 || !ValidHTTPCredentialRecipe(c.Recipe) {
			return invalid
		}
		httpCredentials[c.Ref] = true
	}
	for i, c := range v.Git.Credentials {
		if c.Ref != fmt.Sprintf("git-credential/%d", i+1) || c.SecretRef != c.Ref+"/secret" || c.Name == "" || c.Origin == "" || !ValidHTTPCredentialRecipe(c.Recipe) {
			return invalid
		}
		gitCredentials[c.Ref] = true
	}
	for i, r := range v.Git.Repositories {
		if r.Ref != fmt.Sprintf("repository/%d", i+1) || r.Aliases == nil || r.Name == "" || r.URL == "" || r.CredentialRef != nil && !gitCredentials[*r.CredentialRef] {
			return invalid
		}
		repositories[r.Ref] = true
	}
	expiry := func(s *string) bool {
		if s == nil {
			return true
		}
		_, err := time.Parse(time.RFC3339Nano, *s)
		return err == nil
	}
	for _, g := range v.MCP.Grants {
		if !agents[g.Agent] || !servers[g.Server] || !expiry(g.ExpiresAt) || (g.Effect != GrantAllow && g.Effect != GrantDeny) || !json.Valid(g.Constraint) {
			return invalid
		}
		constrained := !bytes.Equal(g.Constraint, []byte("null"))
		if constrained && !strings.HasPrefix(string(g.Constraint), "{") || g.UpstreamName == nil && constrained || g.ReadOnly && (g.Effect != GrantAllow || g.UpstreamName != nil || constrained) {
			return invalid
		}
	}
	for _, g := range v.HTTP.Grants {
		if !agents[g.Agent] || !expiry(g.ExpiresAt) || !validConfigHTTPPolicy(g.Policy) || g.Policy.CredentialRef != nil && !httpCredentials[*g.Policy.CredentialRef] {
			return invalid
		}
	}
	for _, g := range v.Git.Grants {
		if !agents[g.Agent] || !repositories[g.Repository] || !expiry(g.ExpiresAt) || !validConfigGitPolicy(g.Policy) {
			return invalid
		}
	}
	return nil
}

// ValidConfigServerCredentials checks declared requirements without material.
func ValidConfigServerCredentials(s ConfigServer) bool {
	var transport struct {
		Kind              TransportKind     `json:"kind"`
		SecretEnvironment map[string]string `json:"secret_environment"`
		Authentication    struct {
			Mode         AuthenticationMode `json:"mode"`
			Registration struct {
				Mode   RegistrationMode        `json:"mode"`
				Method TokenEndpointAuthMethod `json:"token_endpoint_auth_method"`
			} `json:"registration"`
		} `json:"authentication"`
	}
	if json.Unmarshal(s.Transport, &transport) != nil {
		return false
	}
	expected := map[string]string{}
	oauth := false
	clientSecret := false
	switch transport.Kind {
	case TransportStdio:
		for _, slot := range transport.SecretEnvironment {
			expected[slot] = s.Ref + "/static/" + slot
		}
	case TransportStreamableHTTP:
		switch transport.Authentication.Mode {
		case AuthenticationNone:
		case AuthenticationBearer:
			expected["bearer"] = s.Ref + "/static/bearer"
		case AuthenticationOAuth:
			oauth = true
			registration := transport.Authentication.Registration
			clientSecret = registration.Mode == RegistrationStatic && registration.Method != TokenEndpointAuthNone
		default:
			return false
		}
	default:
		return false
	}
	return maps.Equal(expected, s.Credentials.Static) && oauth == s.Credentials.OAuthAuthorization && clientSecret == (s.Credentials.OAuthClientSecret != nil) && (s.Credentials.OAuthClientSecret == nil || *s.Credentials.OAuthClientSecret == s.Ref+"/oauth/client_secret")
}

func validConfigHTTPPolicy(p ConfigHTTPPolicy) bool {
	if p.Version != HTTPPolicyVersion {
		return false
	}
	allow := p.Type == HTTPAllowRequests || p.Type == HTTPAllowTunnel
	if allow != (p.AllowPrivate != nil) || p.CredentialRef != nil && p.Type != HTTPAllowRequests {
		return false
	}
	switch p.Type {
	case HTTPBlockDestination, HTTPAllowTunnel:
		return p.Request == nil && p.Destination != nil && p.Destination.Host != "" && p.Destination.Port > 0
	case HTTPBlockRequests, HTTPAllowRequests:
		if p.Destination != nil || p.Request == nil {
			return false
		}
		r := p.Request
		if r.Origin.Host == "" || r.Origin.Port == 0 || r.Origin.Scheme != "http" && r.Origin.Scheme != "https" || p.CredentialRef != nil && r.Origin.Scheme != "https" {
			return false
		}
		if r.Methods.Any && r.Methods.Values != nil || !r.Methods.Any && (len(r.Methods.Values) == 0 || len(r.Methods.Values) > HTTPPolicyMethods) {
			return false
		}
		switch r.Path.Kind {
		case HTTPPathAny:
			return r.Path.Value == ""
		case HTTPPathExact, HTTPPathPrefix:
			return strings.HasPrefix(r.Path.Value, "/")
		default:
			return false
		}
	default:
		return false
	}
}

func validConfigGitPolicy(p GitPolicy) bool {
	if p.Version != 1 || p.Refs == nil || len(p.Refs) > GitRules || len(p.Refs) > 0 && !p.Read {
		return false
	}
	for _, rule := range p.Refs {
		if rule.Ref.Kind != "exact" && rule.Ref.Kind != "prefix" || rule.Ref.Value == "" || len(rule.Actions) == 0 || len(rule.Actions) > 3 {
			return false
		}
		seen := map[string]bool{}
		for _, action := range rule.Actions {
			if action != "create" && action != "update" && action != "delete" || seen[action] {
				return false
			}
			seen[action] = true
		}
	}
	return true
}
