package contract

import "encoding/json"

// Configuration format versions are independent of database and policy versions.
const (
	ConfigExportFormat  = "agent-gateway-config"
	ConfigExportVersion = 1
	ConfigExportPath    = "/api/v2/config/export"
	// Covers the combined bounded server and MCP/HTTP/Git policy inventories.
	ConfigExportMaxBytes = 512 * 1024 * 1024
)

// ConfigExport contains configuration only. No installation authority, material,
// availability, revisions, history, or process-local state belongs in this type.
type ConfigExport struct {
	Format  string        `json:"format"`
	Version int           `json:"version"`
	Agents  []ConfigAgent `json:"agents"`
	MCP     ConfigMCP     `json:"mcp"`
	HTTP    ConfigHTTP    `json:"http"`
	Git     ConfigGit     `json:"git"`
}

type ConfigAgent struct {
	Ref         string              `json:"ref"`
	DisplayName string              `json:"display_name"`
	State       PrincipalState      `json:"state"`
	Visibility  PrincipalVisibility `json:"visibility"`
	HTTPDefault HTTPDefault         `json:"http_default"`
}

type ConfigMCP struct {
	Servers []ConfigServer   `json:"servers"`
	Grants  []ConfigMCPGrant `json:"grants"`
}

type ConfigServer struct {
	Ref         string                  `json:"ref"`
	Namespace   string                  `json:"namespace"`
	DisplayName string                  `json:"display_name"`
	Enabled     bool                    `json:"enabled"`
	Deleted     bool                    `json:"deleted"`
	Transport   json.RawMessage         `json:"transport"`
	Credentials ConfigServerCredentials `json:"credentials"`
}

// Static maps declared slot names to portable secret references, never values.
// OAuth authorization is fresh-install work, not exported token authority.
type ConfigServerCredentials struct {
	Static             map[string]string `json:"static"`
	OAuthClientSecret  *string           `json:"oauth_client_secret"`
	OAuthAuthorization bool              `json:"oauth_authorization"`
}

type ConfigMCPGrant struct {
	ID           string          `json:"-"`
	Agent        string          `json:"agent"`
	Server       string          `json:"server"`
	Description  *string         `json:"description"`
	Effect       GrantEffect     `json:"effect"`
	UpstreamName *string         `json:"upstream_name"`
	Constraint   json.RawMessage `json:"constraint"`
	ReadOnly     bool            `json:"read_only"`
	ExpiresAt    *string         `json:"expires_at"`
}

type ConfigHTTP struct {
	Credentials []ConfigHTTPCredential `json:"credentials"`
	Grants      []ConfigHTTPGrant      `json:"grants"`
}

type ConfigHTTPCredential struct {
	Ref string `json:"ref"`
	HTTPCredentialDefinition
	SecretRef string `json:"secret_ref"`
}

// HTTP policy keeps the v1 selector union, replacing credential_id with a
// document-local credential_ref. Neither field may be interpreted as material.
type ConfigHTTPPolicy struct {
	Version       int                      `json:"version"`
	Type          HTTPGrantType            `json:"type"`
	Destination   *HTTPDestinationSelector `json:"destination,omitempty"`
	Request       *HTTPRequestSelector     `json:"request,omitempty"`
	AllowPrivate  *bool                    `json:"allow_private,omitempty"`
	CredentialRef *string                  `json:"credential_ref,omitempty"`
}

type ConfigHTTPGrant struct {
	ID          string           `json:"-"`
	Agent       string           `json:"agent"`
	Description *string          `json:"description"`
	Policy      ConfigHTTPPolicy `json:"policy"`
	ExpiresAt   *string          `json:"expires_at"`
}

type ConfigGit struct {
	Credentials    []ConfigGitCredential `json:"credentials"`
	Repositories   []ConfigGitRepository `json:"repositories"`
	Grants         []ConfigGitGrant      `json:"grants"`
	RoutingOrigins []string              `json:"routing_origins"`
}

type ConfigGitCredential struct {
	Ref string `json:"ref"`
	GitCredentialDefinition
	SecretRef string `json:"secret_ref"`
}

type ConfigGitRepository struct {
	Ref           string   `json:"ref"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Aliases       []string `json:"aliases"`
	CredentialRef *string  `json:"credential_ref"`
	Deleted       bool     `json:"deleted"`
}

type ConfigGitGrant struct {
	ID          string    `json:"-"`
	Agent       string    `json:"agent"`
	Repository  string    `json:"repository"`
	Description *string   `json:"description"`
	Policy      GitPolicy `json:"policy"`
	ExpiresAt   *string   `json:"expires_at"`
}
