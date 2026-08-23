package contract

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
	HTTPRegular           LimitStatus `json:"http_regular"`
	HTTPControlAuth       LimitStatus `json:"http_control_auth"`
	HTTPAdmin             LimitStatus `json:"http_admin"`
	HTTPHealth            LimitStatus `json:"http_health"`
	MCPWork               LimitStatus `json:"mcp_work"`
	MCPStreams            LimitStatus `json:"mcp_streams"`
	AdminSessions         LimitStatus `json:"admin_sessions"`
	LegacySessions        LimitStatus `json:"legacy_sessions"`
	EventStreams          LimitStatus `json:"event_streams"`
	BackupWork            LimitStatus `json:"backup_work"`
	BackupRecords         LimitStatus `json:"backup_records"`
	AdminCredentials      LimitStatus `json:"admin_credentials"`
	IdempotencyRecords    LimitStatus `json:"idempotency_records"`
	KeyringCandidates     LimitStatus `json:"keyring_candidates"`
	KeyringWork           LimitStatus `json:"keyring_work"`
	DatabaseBytes         LimitStatus `json:"database_bytes"`
	ServerIdentities      LimitStatus `json:"server_identities"`
	Servers               LimitStatus `json:"servers"`
	DownstreamRuntimes    LimitStatus `json:"downstream_runtimes"`
	ServerReconciliations LimitStatus `json:"server_reconciliations"`
	CatalogTraversals     LimitStatus `json:"catalog_traversals"`
	OAuthFlows            LimitStatus `json:"oauth_flows"`
	OAuthCallbackWork     LimitStatus `json:"oauth_callback_work"`
	S2IdempotencyRecords  LimitStatus `json:"s2_idempotency_records"`
	ActiveTools           LimitStatus `json:"active_tools"`
	DurableToolIdentities LimitStatus `json:"durable_tool_identities"`
	DownstreamDispatch    LimitStatus `json:"downstream_dispatch"`
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

type AdminSessionCreated struct {
	CSRFToken         string `json:"csrf_token"`
	IdleExpiresAt     string `json:"idle_expires_at"`
	AbsoluteExpiresAt string `json:"absolute_expires_at"`
}

type Invalidation struct {
	Kind       InvalidationKind `json:"kind"`
	ResourceID *string          `json:"resource_id"`
}
