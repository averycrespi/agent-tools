package contract

import "encoding/json"

// ConfigV2Version is the sole portable format version, independent of storage
// and policy dialect versions. Version-one parsing and execution are retired.
const ConfigV2Version = 2

// ConfigV2KeyBindings bounds permanent typed-key bindings, including live rows,
// deleted definitions and reservations. MCP namespace capacity remains separate.
const ConfigV2KeyBindings = 1 << 20

// ConfigReference is a typed portable key, never an installation identity. The
// fixed gateway_mcp target has no key and is not a normal server definition.
type ConfigReference struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
}

// ConfigDefinition is identical as an individual document and a bundle member.
// Only git_routing is a singleton without a key. A reservation has null spec and
// permanently retains a deleted resource key; it does not recreate that resource.
type ConfigDefinition struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Kind     string          `json:"kind"`
	Key      string          `json:"key,omitempty"`
	Reserved bool            `json:"reserved,omitempty"`
	Spec     json.RawMessage `json:"spec"`
}

type ConfigBundle struct {
	Format    string             `json:"format"`
	Version   int                `json:"version"`
	Resources []ConfigDefinition `json:"resources"`
}

type ConfigAgentSpec struct {
	DisplayName string              `json:"display_name"`
	State       PrincipalState      `json:"state"`
	Visibility  PrincipalVisibility `json:"visibility"`
	HTTPDefault HTTPDefault         `json:"http_default"`
}

type ConfigServerSpec struct {
	DisplayName string                  `json:"display_name"`
	Enabled     bool                    `json:"enabled"`
	Deleted     bool                    `json:"deleted"`
	Transport   json.RawMessage         `json:"transport"`
	Credentials ConfigServerCredentials `json:"credentials"`
}

type ConfigMCPGrantSpec struct {
	Agent        ConfigReference `json:"agent"`
	Server       ConfigReference `json:"server"`
	Description  *string         `json:"description"`
	Effect       GrantEffect     `json:"effect"`
	UpstreamName *string         `json:"upstream_name"`
	Constraint   json.RawMessage `json:"constraint"`
	ReadOnly     bool            `json:"read_only"`
	ExpiresAt    *string         `json:"expires_at"`
}

type ConfigHTTPCredentialSpec struct {
	HTTPCredentialDefinition
	Deleted   bool    `json:"deleted"`
	SecretRef *string `json:"secret_ref"`
}

type ConfigGitCredentialSpec struct {
	GitCredentialDefinition
	Deleted   bool    `json:"deleted"`
	SecretRef *string `json:"secret_ref"`
}

type ConfigV2HTTPPolicy struct {
	Version      int                      `json:"version"`
	Type         HTTPGrantType            `json:"type"`
	Destination  *HTTPDestinationSelector `json:"destination,omitempty"`
	Request      *HTTPRequestSelector     `json:"request,omitempty"`
	AllowPrivate *bool                    `json:"allow_private,omitempty"`
	Credential   *ConfigReference         `json:"credential,omitempty"`
}

type ConfigHTTPGrantSpec struct {
	Agent       ConfigReference    `json:"agent"`
	Description *string            `json:"description"`
	Policy      ConfigV2HTTPPolicy `json:"policy"`
	ExpiresAt   *string            `json:"expires_at"`
}

type ConfigGitRepositorySpec struct {
	Name       string           `json:"name"`
	URL        string           `json:"url"`
	Aliases    []string         `json:"aliases"`
	Credential *ConfigReference `json:"credential"`
	Deleted    bool             `json:"deleted"`
}

type ConfigGitGrantSpec struct {
	Agent       ConfigReference `json:"agent"`
	Repository  ConfigReference `json:"repository"`
	Description *string         `json:"description"`
	Policy      GitPolicy       `json:"policy"`
	ExpiresAt   *string         `json:"expires_at"`
}

type ConfigGitRoutingSpec struct {
	Origins []string `json:"origins"`
}
