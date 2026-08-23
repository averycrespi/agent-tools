package contract

import "encoding/json"

type Transport interface {
	isTransport()
}

type StdioTransport struct {
	Kind              TransportKind     `json:"kind"`
	Executable        string            `json:"executable"`
	Arguments         []string          `json:"arguments"`
	WorkingDirectory  string            `json:"working_directory"`
	Environment       map[string]string `json:"environment"`
	SecretEnvironment map[string]string `json:"secret_environment"`
}

func (StdioTransport) isTransport() {}

type StreamableHTTPTransport struct {
	Kind           TransportKind      `json:"kind"`
	URL            string             `json:"url"`
	ProtocolMode   ProtocolMode       `json:"protocol_mode"`
	Authentication HTTPAuthentication `json:"authentication"`
}

func (StreamableHTTPTransport) isTransport() {}

type HTTPAuthentication interface {
	isHTTPAuthentication()
}

type NoAuthentication struct {
	Mode AuthenticationMode `json:"mode"`
}

func (NoAuthentication) isHTTPAuthentication() {}

type BearerAuthentication struct {
	Mode AuthenticationMode `json:"mode"`
}

func (BearerAuthentication) isHTTPAuthentication() {}

type OAuthAuthentication struct {
	Mode                 AuthenticationMode `json:"mode"`
	Registration         OAuthRegistration  `json:"registration"`
	TrustedOrigins       []string           `json:"trusted_origins"`
	RequestOfflineAccess bool               `json:"request_offline_access"`
}

func (OAuthAuthentication) isHTTPAuthentication() {}

type OAuthRegistration interface {
	isOAuthRegistration()
}

type StaticOAuthRegistration struct {
	Mode                    RegistrationMode        `json:"mode"`
	Issuer                  *string                 `json:"issuer"`
	ClientID                string                  `json:"client_id"`
	TokenEndpointAuthMethod TokenEndpointAuthMethod `json:"token_endpoint_auth_method"`
}

func (StaticOAuthRegistration) isOAuthRegistration() {}

type DynamicOAuthRegistration struct {
	Mode   RegistrationMode `json:"mode"`
	Issuer *string          `json:"issuer"`
}

func (DynamicOAuthRegistration) isOAuthRegistration() {}

type ServerCreate struct {
	Namespace   string    `json:"namespace"`
	DisplayName string    `json:"display_name"`
	Enabled     bool      `json:"enabled"`
	Transport   Transport `json:"transport"`
}

type ServerPatch struct {
	DisplayName *string   `json:"display_name,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
	Transport   Transport `json:"transport,omitempty"`
}

type CredentialRevisions struct {
	StaticCredential string `json:"static_credential"`
	OAuthClient      string `json:"oauth_client"`
	OAuthTokens      string `json:"oauth_tokens"`
}

type ServerRuntime struct {
	State          RuntimeState  `json:"state"`
	Reason         *PublicReason `json:"reason"`
	RuntimeID      *string       `json:"runtime_id"`
	Reconciliation LimitStatus   `json:"reconciliation"`
	Dispatch       LimitStatus   `json:"dispatch"`
}

type ServerCatalog struct {
	DurableState     DurableCatalogState `json:"durable_state"`
	ActiveState      ActiveCatalogState  `json:"active_state"`
	DurableRevision  *string             `json:"durable_revision"`
	ActiveRevision   *string             `json:"active_revision"`
	DurableToolCount int64               `json:"durable_tool_count"`
	ActiveToolCount  int64               `json:"active_tool_count"`
	LastSuccessAt    *string             `json:"last_success_at"`
	Traversal        LimitStatus         `json:"traversal"`
}

type Server struct {
	ID                  string                `json:"id"`
	Namespace           string                `json:"namespace"`
	DisplayName         string                `json:"display_name"`
	DesiredState        DesiredServerState    `json:"desired_state"`
	DesiredRevision     string                `json:"desired_revision"`
	Transport           Transport             `json:"transport"`
	CredentialRevisions CredentialRevisions   `json:"credential_revisions"`
	CredentialState     ServerCredentialState `json:"credential_state"`
	Runtime             ServerRuntime         `json:"runtime"`
	Catalog             ServerCatalog         `json:"catalog"`
	CreatedAt           string                `json:"created_at"`
	UpdatedAt           string                `json:"updated_at"`
	DeletedAt           *string               `json:"deleted_at"`
}

type ServerOperation struct {
	ID                        string               `json:"id"`
	ServerID                  string               `json:"server_id"`
	Kind                      ServerOperationKind  `json:"kind"`
	TargetDesiredRevision     string               `json:"target_desired_revision"`
	TargetCredentialRevisions CredentialRevisions  `json:"target_credential_revisions"`
	State                     ServerOperationState `json:"state"`
	Reason                    *PublicReason        `json:"reason"`
	CreatedAt                 string               `json:"created_at"`
	StartedAt                 *string              `json:"started_at"`
	FinishedAt                *string              `json:"finished_at"`
}

type ServerAuthFlow struct {
	ID                    string        `json:"id"`
	ServerID              string        `json:"server_id"`
	FlowState             AuthFlowState `json:"flow_state"`
	TargetDesiredRevision string        `json:"target_desired_revision"`
	RegistrationRevision  string        `json:"registration_revision"`
	CreatedAt             string        `json:"created_at"`
	ExpiresAt             string        `json:"expires_at"`
	FinishedAt            *string       `json:"finished_at"`
	Reason                *PublicReason `json:"reason"`
}

type NormalizedToolAnnotations struct {
	Title           *string `json:"title"`
	ReadOnlyHint    bool    `json:"readOnlyHint"`
	DestructiveHint bool    `json:"destructiveHint"`
	IdempotentHint  bool    `json:"idempotentHint"`
	OpenWorldHint   bool    `json:"openWorldHint"`
}

type NormalizedToolDescriptor struct {
	Name         string                    `json:"name"`
	Title        *string                   `json:"title,omitempty"`
	Description  *string                   `json:"description,omitempty"`
	InputSchema  json.RawMessage           `json:"inputSchema"`
	OutputSchema json.RawMessage           `json:"outputSchema,omitempty"`
	Annotations  NormalizedToolAnnotations `json:"annotations"`
}

type ToolDescriptor struct {
	ID              string                   `json:"id"`
	ServerID        string                   `json:"server_id"`
	UpstreamName    string                   `json:"upstream_name"`
	ExternalName    string                   `json:"external_name"`
	Descriptor      NormalizedToolDescriptor `json:"descriptor"`
	Fingerprint     string                   `json:"fingerprint"`
	CatalogRevision string                   `json:"catalog_revision"`
	FirstSeenAt     string                   `json:"first_seen_at"`
	LastSeenAt      string                   `json:"last_seen_at"`
	RetiredAt       *string                  `json:"retired_at"`
}

type CatalogSummary struct {
	ActiveState      AggregateCatalogState `json:"active_state"`
	ActiveGeneration string                `json:"active_generation"`
	ChangedAt        *string               `json:"changed_at"`
	IssueCount       int64                 `json:"issue_count"`
}

type CatalogPage struct {
	Catalog    CatalogSummary   `json:"catalog"`
	Items      []ToolDescriptor `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

type ServerMutation struct {
	Server    Server           `json:"server"`
	Operation *ServerOperation `json:"operation"`
}

type ServerOperationCreate struct {
	Kind ServerOperationKind `json:"kind"`
}

type ServerOperationMutation struct {
	Operation ServerOperation `json:"operation"`
}

type CredentialReplacement interface {
	isCredentialReplacement()
}

type StaticCredentialReplacement struct {
	Kind             ServerCredentialKind `json:"kind"`
	ExpectedRevision string               `json:"expected_revision"`
	Values           map[string]string    `json:"values"`
}

func (StaticCredentialReplacement) isCredentialReplacement() {}

type OAuthClientCredentialReplacement struct {
	Kind             ServerCredentialKind `json:"kind"`
	ExpectedRevision string               `json:"expected_revision"`
	ClientSecret     string               `json:"client_secret"`
}

func (OAuthClientCredentialReplacement) isCredentialReplacement() {}

type CredentialReplacementResult struct {
	ServerID           string               `json:"server_id"`
	Kind               ServerCredentialKind `json:"kind"`
	CredentialRevision string               `json:"credential_revision"`
	Operation          ServerOperation      `json:"operation"`
}

type AuthFlowCreation struct {
	Flow             ServerAuthFlow `json:"flow"`
	AuthorizationURL string         `json:"authorization_url"`
}
