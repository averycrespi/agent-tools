package contract

import "encoding/json"

type ProblemEnvelope struct {
	Status int         `json:"status"`
	Code   ProblemCode `json:"code"`
	Title  string      `json:"title"`
}

type AdminCredential struct {
	ID          string           `json:"id"`
	Fingerprint string           `json:"fingerprint"`
	CreatedAt   string           `json:"created_at"`
	ExpiresAt   *string          `json:"expires_at"`
	NonExpiring bool             `json:"non_expiring"`
	Status      CredentialStatus `json:"status"`
	Revision    string           `json:"revision"`
}

type CreatedAdminCredential struct {
	AdminCredential
	Bearer string `json:"bearer"`
}

type AdminAuthority struct {
	Revision string `json:"revision"`
}

type AdminCredentialRotationCompletion struct {
	ReplacementID string `json:"replacement_id"`
}

type AdminCredentialRotationResult struct {
	OldCredential AdminCredential `json:"old_credential"`
	NewCredential AdminCredential `json:"new_credential"`
}

type Backup struct {
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	InstallationID string `json:"installation_id"`
	SchemaVersion  string `json:"schema_version"`
	SourceRevision string `json:"source_revision"`
	SizeBytes      int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
}

type Collection[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type AgentCredential struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Revision    string `json:"revision"`
	CreatedAt   string `json:"created_at"`
}

type Principal struct {
	ID                 string              `json:"id"`
	DisplayName        string              `json:"display_name"`
	State              PrincipalState      `json:"state"`
	Visibility         PrincipalVisibility `json:"visibility"`
	Revision           string              `json:"revision"`
	CredentialRevision string              `json:"credential_revision"`
	Credential         *AgentCredential    `json:"credential"`
	CreatedAt          string              `json:"created_at"`
	UpdatedAt          string              `json:"updated_at"`
}

type PrincipalCreation struct {
	Principal    Principal `json:"principal"`
	DefaultGrant Grant     `json:"default_grant"`
}

type AgentCredentialCreation struct {
	Principal Principal `json:"principal"`
	Bearer    string    `json:"bearer"`
}

type Grant struct {
	ID           string           `json:"id"`
	Description  *string          `json:"description"`
	Revision     string           `json:"revision"`
	PrincipalID  string           `json:"principal_id"`
	Effect       GrantEffect      `json:"effect"`
	ServerID     string           `json:"server_id"`
	UpstreamName *string          `json:"upstream_name"`
	Constraint   *json.RawMessage `json:"constraint"`
	ExpiresAt    *string          `json:"expires_at"`
	State        GrantState       `json:"state"`
	CreatedAt    string           `json:"created_at"`
}

type AuthorizationResult struct {
	Decision              AuthorizationDecision `json:"decision"`
	AuthorizationRevision string                `json:"authorization_revision"`
	EvaluatedAt           string                `json:"evaluated_at"`
	GrantID               *string               `json:"grant_id"`
}

type ProcessStatus struct {
	State     ProcessState `json:"state"`
	Ready     bool         `json:"ready"`
	StartedAt string       `json:"started_at"`
}

type SQLiteStatus struct {
	State         SQLiteState `json:"state"`
	SchemaVersion string      `json:"schema_version"`
	Revision      string      `json:"revision"`
	Latched       bool        `json:"latched"`
}

type KeyringStatus struct {
	Capability KeyringCapability `json:"capability"`
}

type LimitStatus struct {
	InUse     int64 `json:"in_use"`
	Limit     int64 `json:"limit"`
	Saturated bool  `json:"saturated"`
}

type LimitsStatus struct {
	HTTPRegular               LimitStatus `json:"http_regular"`
	HTTPControlAuth           LimitStatus `json:"http_control_auth"`
	HTTPAdmin                 LimitStatus `json:"http_admin"`
	HTTPHealth                LimitStatus `json:"http_health"`
	MCPWork                   LimitStatus `json:"mcp_work"`
	MCPStreams                LimitStatus `json:"mcp_streams"`
	AdminSessions             LimitStatus `json:"admin_sessions"`
	LegacySessions            LimitStatus `json:"legacy_sessions"`
	EventStreams              LimitStatus `json:"event_streams"`
	BackupWork                LimitStatus `json:"backup_work"`
	BackupRecords             LimitStatus `json:"backup_records"`
	AdminCredentials          LimitStatus `json:"admin_credentials"`
	IdempotencyRecords        LimitStatus `json:"idempotency_records"`
	KeyringCandidates         LimitStatus `json:"keyring_candidates"`
	KeyringWork               LimitStatus `json:"keyring_work"`
	DatabaseBytes             LimitStatus `json:"database_bytes"`
	ServerIdentities          LimitStatus `json:"server_identities"`
	Servers                   LimitStatus `json:"servers"`
	DownstreamRuntimes        LimitStatus `json:"downstream_runtimes"`
	ServerReconciliations     LimitStatus `json:"server_reconciliations"`
	CatalogTraversals         LimitStatus `json:"catalog_traversals"`
	OAuthFlows                LimitStatus `json:"oauth_flows"`
	OAuthCallbackWork         LimitStatus `json:"oauth_callback_work"`
	S2IdempotencyRecords      LimitStatus `json:"s2_idempotency_records"`
	ActiveTools               LimitStatus `json:"active_tools"`
	DurableToolIdentities     LimitStatus `json:"durable_tool_identities"`
	DownstreamDispatch        LimitStatus `json:"downstream_dispatch"`
	Principals                LimitStatus `json:"principals"`
	Grants                    LimitStatus `json:"grants"`
	GrantRequests             LimitStatus `json:"grant_requests"`
	GrantRequestEvidenceBytes LimitStatus `json:"grant_request_evidence_bytes"`
}

type BackupStatus struct {
	State           BackupState `json:"state"`
	LastCompletedAt *string     `json:"last_completed_at"`
}

type ProtocolStatus struct {
	Modern    string        `json:"modern"`
	Legacy    string        `json:"legacy"`
	AgentAuth AgentAuthMode `json:"agent_auth"`
}

type SystemStatus struct {
	Process   ProcessStatus  `json:"process"`
	SQLite    SQLiteStatus   `json:"sqlite"`
	Keyring   KeyringStatus  `json:"keyring"`
	Limits    LimitsStatus   `json:"limits"`
	Backup    BackupStatus   `json:"backup"`
	Protocols ProtocolStatus `json:"protocols"`
}

type AdminSessionBootstrap struct {
	CSRFToken         string `json:"csrf_token"`
	IdleExpiresAt     string `json:"idle_expires_at"`
	AbsoluteExpiresAt string `json:"absolute_expires_at"`
}

type AdminSessionCreated = AdminSessionBootstrap

type Invalidation struct {
	Kind       InvalidationKind `json:"kind"`
	ResourceID *string          `json:"resource_id"`
}
