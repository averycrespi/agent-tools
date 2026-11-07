package configdefinition

import (
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

// ValidateBundle requires a complete retained graph. Single definitions need only
// typed dependency references; resolving those belongs to the future import owner.
func ValidateBundle(b contract.ConfigBundle) error {
	if b.Format != contract.ConfigExportFormat || b.Version != contract.ConfigV2Version || b.Resources == nil || len(b.Resources) > MaxKeyBindings+1025 {
		return ErrInvalid
	}
	present := map[string]bool{}
	counts := map[string]int{}
	retainedCredentials := map[string]int{}
	keyBindings := 0
	namespaces := 0
	for _, d := range b.Resources {
		if _, err := Normalize(d); err != nil {
			return ErrInvalid
		}
		id := identity(d.Kind, d.Key)
		if _, ok := present[id]; ok {
			return ErrInvalid
		}
		deletedCredential := false
		if d.Kind == "http_credential" || d.Kind == "git_credential" {
			var state struct {
				Deleted bool `json:"deleted"`
			}
			_ = json.Unmarshal(d.Spec, &state)
			deletedCredential = state.Deleted
		}
		if !d.Reserved && (d.Kind == "http_credential" || d.Kind == "git_credential") {
			retainedCredentials[d.Kind]++
			maximum := contract.HTTPCredentialIdentities
			if d.Kind == "git_credential" {
				maximum = contract.GitCredentialIdentities
			}
			if retainedCredentials[d.Kind] > maximum {
				return ErrInvalid
			}
		}
		present[id] = !d.Reserved && !deletedCredential
		if d.Kind == "mcp_server" {
			namespaces++
			if namespaces > 1024 {
				return ErrInvalid
			}
		} else if d.Kind != "git_routing" {
			keyBindings++
			if keyBindings > MaxKeyBindings {
				return ErrInvalid
			}
		}
		if !d.Reserved && !deletedCredential {
			counts[d.Kind]++
			if counts[d.Kind] > limits[d.Kind] {
				return ErrInvalid
			}
		}
	}
	if counts["git_routing"] != 1 {
		return ErrInvalid
	}
	resolve := func(r contract.ConfigReference) bool {
		return reference(r, "gateway_mcp") || present[identity(r.Kind, r.Key)]
	}
	resolveTarget := func(r contract.ConfigReference) bool {
		_, retained := present[identity(r.Kind, r.Key)]
		return resolve(r) || retained && (r.Kind == "mcp_server" || r.Kind == "git_repository")
	}
	// Pure owner validation also checks cross-resource credential containment,
	// locator collisions, active capacities and routing independently of policy.
	graph := contract.ConfigExport{}
	graph.Git.RoutingOrigins = []string{}
	serverSpecs := []contract.ConfigServer{}
	for _, d := range b.Resources {
		if d.Reserved {
			continue
		}
		switch d.Kind {
		case "agent":
			var s contract.ConfigAgentSpec
			_ = json.Unmarshal(d.Spec, &s)
			graph.Agents = append(graph.Agents, contract.ConfigAgent{Ref: identity(d.Kind, d.Key), DisplayName: s.DisplayName, State: s.State, Visibility: s.Visibility, HTTPDefault: s.HTTPDefault})
		case "mcp_server":
			var s contract.ConfigServerSpec
			_ = json.Unmarshal(d.Spec, &s)
			serverSpecs = append(serverSpecs, contract.ConfigServer{Namespace: d.Key, DisplayName: s.DisplayName, Enabled: s.Enabled, Deleted: s.Deleted, Transport: s.Transport, Credentials: s.Credentials})
		case "mcp_grant":
			var s contract.ConfigMCPGrantSpec
			_ = json.Unmarshal(d.Spec, &s)
			if !resolve(s.Agent) || !resolveTarget(s.Server) {
				return ErrInvalid
			}
		case "http_credential":
			var s contract.ConfigHTTPCredentialSpec
			_ = json.Unmarshal(d.Spec, &s)
			graph.HTTP.Credentials = append(graph.HTTP.Credentials, contract.ConfigHTTPCredential{Ref: identity(d.Kind, d.Key), HTTPCredentialDefinition: s.HTTPCredentialDefinition})
		case "http_grant":
			var s contract.ConfigHTTPGrantSpec
			_ = json.Unmarshal(d.Spec, &s)
			if !resolve(s.Agent) || s.Policy.Credential != nil && !resolve(*s.Policy.Credential) {
				return ErrInvalid
			}
			var credential *string
			if s.Policy.Credential != nil {
				ref := identity(s.Policy.Credential.Kind, s.Policy.Credential.Key)
				credential = &ref
			}
			graph.HTTP.Grants = append(graph.HTTP.Grants, contract.ConfigHTTPGrant{Description: s.Description, ExpiresAt: s.ExpiresAt, Policy: contract.ConfigHTTPPolicy{Version: s.Policy.Version, Type: s.Policy.Type, Destination: s.Policy.Destination, Request: s.Policy.Request, AllowPrivate: s.Policy.AllowPrivate, CredentialRef: credential}})
		case "git_credential":
			var s contract.ConfigGitCredentialSpec
			_ = json.Unmarshal(d.Spec, &s)
			graph.Git.Credentials = append(graph.Git.Credentials, contract.ConfigGitCredential{Ref: identity(d.Kind, d.Key), GitCredentialDefinition: s.GitCredentialDefinition})
		case "git_repository":
			var s contract.ConfigGitRepositorySpec
			_ = json.Unmarshal(d.Spec, &s)
			if s.Credential != nil && !resolve(*s.Credential) {
				return ErrInvalid
			}
			var credential *string
			if s.Credential != nil {
				ref := identity(s.Credential.Kind, s.Credential.Key)
				credential = &ref
			}
			graph.Git.Repositories = append(graph.Git.Repositories, contract.ConfigGitRepository{Ref: identity(d.Kind, d.Key), Name: s.Name, URL: s.URL, Aliases: s.Aliases, CredentialRef: credential, Deleted: s.Deleted})
		case "git_grant":
			var s contract.ConfigGitGrantSpec
			_ = json.Unmarshal(d.Spec, &s)
			if !resolve(s.Agent) || !resolveTarget(s.Repository) {
				return ErrInvalid
			}
		case "git_routing":
			var s contract.ConfigGitRoutingSpec
			_ = json.Unmarshal(d.Spec, &s)
			graph.Git.RoutingOrigins = s.Origins
		}
	}
	if servers.ValidateImportedConfiguration(serverSpecs) != nil || authorization.ValidateImportedConfiguration(graph) != nil {
		return ErrInvalid
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > contract.ConfigExportMaxBytes {
		return ErrInvalid
	}
	return nil
}
