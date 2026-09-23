package contract

import "encoding/json"

// HTTP grants remain separate from MCP grants and principal settings.
type HTTPGrant struct {
	ID          string          `json:"id"`
	PrincipalID string          `json:"principal_id"`
	Description *string         `json:"description"`
	Revision    string          `json:"revision"`
	Policy      json.RawMessage `json:"policy"`
	ExpiresAt   *string         `json:"expires_at"`
	State       GrantState      `json:"state"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type HTTPGrantTableItem struct {
	Grant                HTTPGrant `json:"grant"`
	PrincipalDisplayName string    `json:"principal_display_name"`
}

type HTTPAccessPreview struct {
	Decision           HTTPDecision `json:"decision"`
	Default            HTTPDefault  `json:"default"`
	PolicyOnly         bool         `json:"policy_only"`
	NetworkVerified    bool         `json:"network_verified"`
	TLSVerified        bool         `json:"tls_verified"`
	MaterialVerified   bool         `json:"material_verified"`
	AdmissionAuthority bool         `json:"admission_authority"`
}
