package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ValidateConfigBundleEnvelope validates finite-response graph facts without
// private domain access. Consumers additionally strictly decode every spec using
// ConfigSpecDestination; full creation policy normalization stays owner-local.
func ValidateConfigBundleEnvelope(b ConfigBundle) error {
	return validateConfigEnvelope(b, true)
}

// ValidateConfigDefinitionEnvelope checks one definition without requiring its
// dependencies in the document. Receiving dependency resolution is backend-owned.
func ValidateConfigDefinitionEnvelope(d ConfigDefinition) error {
	return validateConfigEnvelope(ConfigBundle{Format: d.Format, Version: d.Version, Resources: []ConfigDefinition{d}}, false)
}

func validateConfigEnvelope(b ConfigBundle, complete bool) error {
	invalid := errors.New("invalid version-2 configuration response")
	if b.Format != ConfigExportFormat || b.Version != ConfigV2Version || b.Resources == nil || len(b.Resources) > ConfigV2KeyBindings+1025 {
		return invalid
	}
	present := map[string]bool{}
	type binding struct {
		ref      ConfigReference
		kind     string
		inactive bool
	}
	dependencies := []binding{}
	counts := map[string]int{}
	retained := map[string]int{}
	keyCount, namespaceCount := 0, 0
	limits := map[string]int{"agent": 128, "mcp_server": 1024, "mcp_grant": 4096, "http_credential": 256, "http_grant": HTTPPolicyGrants, "git_credential": GitCredentials, "git_repository": GitRepositoryIdentities, "git_grant": GitGrants, "git_routing": 1}
	for _, d := range b.Resources {
		if d.Format != ConfigExportFormat || d.Version != ConfigV2Version {
			return invalid
		}
		switch d.Kind {
		case "git_routing":
			if d.Key != "" || d.Reserved {
				return invalid
			}
		case "mcp_server":
			if !ValidResourceKey(d.Key) || len(d.Key) > 32 || d.Key == SyntheticServerNamespace {
				return invalid
			}
			namespaceCount++
		case "agent", "mcp_grant", "http_credential", "http_grant", "git_credential", "git_repository", "git_grant":
			if !ValidResourceKey(d.Key) {
				return invalid
			}
			keyCount++
		default:
			return invalid
		}
		if keyCount > ConfigV2KeyBindings || namespaceCount > 1024 {
			return invalid
		}
		id := d.Kind + "/" + d.Key
		if _, exists := present[id]; exists {
			return invalid
		}
		present[id] = !d.Reserved
		if d.Reserved {
			if !bytes.Equal(bytes.TrimSpace(d.Spec), []byte("null")) {
				return invalid
			}
			continue
		}
		if int64(len(d.Spec)) > ConfigSpecMaxBytes(d.Kind) {
			return invalid
		}
		spec := ConfigSpecDestination(d.Kind)
		if json.Unmarshal(d.Spec, spec) != nil {
			return invalid
		}
		deletedCredential := false
		switch s := spec.(type) {
		case *ConfigAgentSpec:
			if _, err := ParsePrincipalState(string(s.State)); err != nil {
				return invalid
			}
			if _, err := ParsePrincipalVisibility(string(s.Visibility)); err != nil {
				return invalid
			}
			if s.DisplayName == "" || s.HTTPDefault != HTTPDefaultAllow && s.HTTPDefault != HTTPDefaultBlock {
				return invalid
			}
		case *ConfigServerSpec:
			if s.DisplayName == "" || s.Credentials.Static == nil {
				return invalid
			}
			if s.Deleted {
				if s.Enabled || !bytes.Equal(bytes.TrimSpace(s.Transport), []byte("null")) || len(s.Credentials.Static) != 0 || s.Credentials.OAuthClientSecret != nil || s.Credentials.OAuthAuthorization {
					return invalid
				}
			} else if !ValidConfigServerCredentials(ConfigServer{Ref: "mcp/" + d.Key, Transport: s.Transport, Credentials: s.Credentials}) {
				return invalid
			}
		case *ConfigMCPGrantSpec:
			dependencies = append(dependencies, binding{s.Agent, "agent", false})
			if s.Server.Kind == "gateway_mcp" {
				if s.Server.Key != "" {
					return invalid
				}
			} else {
				dependencies = append(dependencies, binding{s.Server, "mcp_server", true})
			}
			constraint := bytes.TrimSpace(s.Constraint)
			constrained := !bytes.Equal(constraint, []byte("null"))
			if s.Effect != GrantAllow && s.Effect != GrantDeny || !configWireExpiry(s.ExpiresAt) || !json.Valid(constraint) || constrained && (len(constraint) == 0 || constraint[0] != '{' || s.UpstreamName == nil) {
				return invalid
			}
			if s.ReadOnly && (s.Effect != GrantAllow || s.UpstreamName != nil || !bytes.Equal(bytes.TrimSpace(s.Constraint), []byte("null"))) {
				return invalid
			}
		case *ConfigHTTPCredentialSpec:
			deletedCredential = s.Deleted
			if s.Name == "" || s.Boundary.Host == "" || s.Boundary.Port == 0 || !ValidHTTPCredentialRecipe(s.Recipe) || !configWireSecret(d, s.Deleted, s.SecretRef) {
				return invalid
			}
		case *ConfigHTTPGrantSpec:
			dependencies = append(dependencies, binding{s.Agent, "agent", false})
			if s.Policy.Credential != nil {
				dependencies = append(dependencies, binding{*s.Policy.Credential, "http_credential", false})
			}
			if !configWireHTTPPolicy(s.Policy) || !configWireExpiry(s.ExpiresAt) {
				return invalid
			}
		case *ConfigGitCredentialSpec:
			deletedCredential = s.Deleted
			if s.Name == "" || s.Origin == "" || !ValidHTTPCredentialRecipe(s.Recipe) || !configWireSecret(d, s.Deleted, s.SecretRef) {
				return invalid
			}
		case *ConfigGitRepositorySpec:
			if s.Name == "" || s.URL == "" || s.Aliases == nil {
				return invalid
			}
			if s.Credential != nil {
				dependencies = append(dependencies, binding{*s.Credential, "git_credential", false})
			}
		case *ConfigGitGrantSpec:
			dependencies = append(dependencies, binding{s.Agent, "agent", false}, binding{s.Repository, "git_repository", true})
			if !configWireGitPolicy(s.Policy) || !configWireExpiry(s.ExpiresAt) {
				return invalid
			}
		case *ConfigGitRoutingSpec:
			if s.Origins == nil || len(s.Origins) > GitRepositories {
				return invalid
			}
		}
		if d.Kind == "http_credential" || d.Kind == "git_credential" {
			retained[d.Kind]++
			maximum := HTTPCredentialIdentities
			if d.Kind == "git_credential" {
				maximum = GitCredentialIdentities
			}
			if retained[d.Kind] > maximum {
				return invalid
			}
			if deletedCredential {
				present[id] = false
				continue
			}
		}
		counts[d.Kind]++
		if counts[d.Kind] > limits[d.Kind] {
			return invalid
		}
	}
	if complete && !present["git_routing/"] {
		return invalid
	}
	for _, dependency := range dependencies {
		r := dependency.ref
		if r.Kind != dependency.kind || !ValidResourceKey(r.Key) {
			return invalid
		}
		if r.Kind == "mcp_server" && (len(r.Key) > 32 || r.Key == SyntheticServerNamespace) {
			return invalid
		}
		live, exists := present[r.Kind+"/"+r.Key]
		if complete && (!exists || !live && !dependency.inactive) {
			return invalid
		}
	}
	return nil
}
func configWireSecret(d ConfigDefinition, deleted bool, ref *string) bool {
	if deleted {
		return ref == nil
	}
	return ref != nil && *ref == d.Kind+"/"+d.Key+"/secret"
}
func configWireExpiry(expiry *string) bool {
	if expiry == nil {
		return true
	}
	parsed, err := time.Parse(AuditTimestampLayout, *expiry)
	return err == nil && parsed.UTC().Format(AuditTimestampLayout) == *expiry
}
func configWireHTTPPolicy(p ConfigV2HTTPPolicy) bool {
	if p.Version != HTTPPolicyVersion {
		return false
	}
	allow := p.Type == HTTPAllowRequests || p.Type == HTTPAllowTunnel
	if !allow && p.AllowPrivate != nil || p.Credential != nil && p.Type != HTTPAllowRequests {
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
		if r.Origin.Host == "" || r.Origin.Port == 0 || r.Origin.Scheme != "http" && r.Origin.Scheme != "https" || p.Credential != nil && r.Origin.Scheme != "https" {
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
func configWireGitPolicy(p GitPolicy) bool {
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
