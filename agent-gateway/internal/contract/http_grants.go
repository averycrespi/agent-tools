package contract

import "encoding/json"

// HTTP resources are separate from the compatible MCP Principal/Grant shapes.
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

type PrincipalHTTPDefault struct {
	PrincipalID string      `json:"principal_id"`
	Default     HTTPDefault `json:"default"`
	Revision    string      `json:"revision"`
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
