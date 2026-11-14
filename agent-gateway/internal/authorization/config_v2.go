package authorization

import (
	"bytes"
	"encoding/json"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
)

// ValidateConfigurationAgent validates authored state without issuing authority.
func ValidateConfigurationAgent(a contract.ConfigAgentSpec) error {
	_, state := contract.ParsePrincipalState(string(a.State))
	_, visibility := contract.ParsePrincipalVisibility(string(a.Visibility))
	if !validDisplayName(a.DisplayName) || state != nil || visibility != nil || a.HTTPDefault != contract.HTTPDefaultBlock && a.HTTPDefault != contract.HTTPDefaultAllow {
		return ErrInvalidInput
	}
	return nil
}

func ValidateConfigurationMCPGrant(g contract.ConfigMCPGrantSpec) error {
	if !validGrantDescription(g.Description) || !validGrantEffect(g.Effect) || !configurationExpiry(g.ExpiresAt) || g.UpstreamName != nil && !validUpstreamName(*g.UpstreamName) {
		return ErrInvalidInput
	}
	constrained := !bytes.Equal(bytes.TrimSpace(g.Constraint), []byte("null"))
	if g.UpstreamName == nil && constrained || g.ReadOnly && (g.Effect != contract.GrantAllow || g.UpstreamName != nil || constrained) {
		return ErrInvalidInput
	}
	if constrained {
		_, err := CompileConstraint(g.Constraint)
		return err
	}
	return nil
}

// ConfigurationHTTPPolicy validates the unchanged policy dialect. The opaque ID
// placeholder is used only by the pure policy decoder, never persisted/exported.
func ConfigurationHTTPPolicy(p contract.ConfigV2HTTPPolicy) (httppolicy.Policy, error) {
	var credential *string
	if p.Credential != nil {
		id := "00000000000000000000000001"
		credential = &id
	}
	raw, err := json.Marshal(contract.HTTPPolicy{Version: p.Version, Type: p.Type, Destination: p.Destination, Request: p.Request, AllowPrivate: p.AllowPrivate, CredentialID: credential})
	if err != nil {
		return httppolicy.Policy{}, err
	}
	return httppolicy.DecodePolicy(raw)
}

func ValidateConfigurationHTTPGrant(g contract.ConfigHTTPGrantSpec) error {
	if !validGrantDescription(g.Description) || !configurationExpiry(g.ExpiresAt) {
		return ErrInvalidInput
	}
	_, err := ConfigurationHTTPPolicy(g.Policy)
	return err
}

func ValidateConfigurationGitRepository(r contract.ConfigGitRepositorySpec) error {
	var credential *string
	if r.Credential != nil {
		id := "00000000000000000000000001"
		credential = &id
	}
	_, err := normalizeGitRepository(contract.GitRepositoryDefinition{Name: r.Name, URL: r.URL, Aliases: r.Aliases, CredentialID: credential})
	return err
}

func ValidateConfigurationGitGrant(g contract.ConfigGitGrantSpec) error {
	if !validGrantDescription(g.Description) || !configurationExpiry(g.ExpiresAt) {
		return ErrInvalidInput
	}
	_, err := gitpolicy.Normalize(g.Policy)
	return err
}

func ValidateConfigurationGitRouting(r contract.ConfigGitRoutingSpec) error {
	_, err := normalizeGitOrigins(r.Origins)
	return err
}
