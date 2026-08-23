package contract

import "time"

const (
	AdminListPageDefault       = 50
	BackupListPageDefault      = 50
	S2ListPageDefault          = 50
	IdempotencyKeyMinimumBytes = 1
)

const (
	HeaderReadDeadline               = 5 * time.Second
	APIHandlerDeadline               = 30 * time.Second
	SQLiteBusyDeadline               = 2 * time.Second
	SSEKeepaliveInterval             = 15 * time.Second
	SSEBlockedWriteDeadline          = 15 * time.Second
	AdminSessionIdleLifetime         = 30 * time.Minute
	AdminSessionAbsoluteLifetime     = 8 * time.Hour
	LegacyIdleLifetime               = 30 * time.Minute
	LegacyAbsoluteLifetime           = 8 * time.Hour
	GracefulShutdownDeadline         = 10 * time.Second
	IdempotencyRetention             = 24 * time.Hour
	CredentialMinimumLifetime        = 5 * time.Minute
	CredentialMaximumLifetime        = 365 * 24 * time.Hour
	OAuthFlowLifetime                = 5 * time.Minute
	DownstreamConnectDeadline        = 10 * time.Second
	OAuthRequestDeadline             = 15 * time.Second
	DownstreamInitializationDeadline = 30 * time.Second
	CatalogPageDeadline              = 15 * time.Second
	CatalogTraversalDeadline         = 60 * time.Second
	MaximumDownstreamCallDeadline    = 60 * time.Second
	StdioGracefulStopDeadline        = 3 * time.Second
	StdioForcedStopDeadline          = 2 * time.Second
	CatalogPollInterval              = 5 * time.Minute
	CatalogPollMaximumJitter         = 30 * time.Second
)

type FixedLimit struct {
	Name    string
	Maximum int64
}

var fixedLimits = []FixedLimit{
	{Name: "request_target_bytes", Maximum: 8 * 1024},
	{Name: "request_header_bytes", Maximum: 32 * 1024},
	{Name: "request_header_count", Maximum: 100},
	{Name: "request_header_value_bytes", Maximum: 8 * 1024},
	{Name: "api_json_body_bytes", Maximum: 1 * 1024 * 1024},
	{Name: "mcp_body_bytes", Maximum: 4 * 1024 * 1024},
	{Name: "json_depth", Maximum: 64},
	{Name: "http_regular", Maximum: 128},
	{Name: "http_control_auth", Maximum: 32},
	{Name: "http_admin", Maximum: 16},
	{Name: "http_health", Maximum: 8},
	{Name: "mcp_work", Maximum: 32},
	{Name: "mcp_streams", Maximum: 32},
	{Name: "admin_sessions", Maximum: 128},
	{Name: "legacy_sessions", Maximum: 128},
	{Name: "event_streams", Maximum: 16},
	{Name: "event_buffered_invalidations", Maximum: 16},
	{Name: "backup_work", Maximum: 1},
	{Name: "backup_records", Maximum: 64},
	{Name: "admin_credentials", Maximum: 128},
	{Name: "admin_list_page", Maximum: 100},
	{Name: "backup_list_page", Maximum: 100},
	{Name: "database_bytes", Maximum: 1 * 1024 * 1024 * 1024},
	{Name: "idempotency_key_bytes", Maximum: 128},
	{Name: "idempotency_records", Maximum: 1024},
	{Name: "opaque_id_bytes", Maximum: 26},
	{Name: "cursor_bytes", Maximum: 512},
	{Name: "sse_frame_bytes", Maximum: 512},
	{Name: "keyring_secret_bytes", Maximum: 256 * 1024},
	{Name: "keyring_chunk_bytes", Maximum: 3000},
	{Name: "keyring_candidates", Maximum: 64},
	{Name: "keyring_work", Maximum: 1},
	{Name: "namespace_bytes", Maximum: 32},
	{Name: "display_name_bytes", Maximum: 256},
	{Name: "stdio_arguments", Maximum: 64},
	{Name: "stdio_environment_entries", Maximum: 32},
	{Name: "stdio_secret_environment_entries", Maximum: 16},
	{Name: "stdio_path_bytes", Maximum: 4 * 1024},
	{Name: "stdio_argument_bytes", Maximum: 4 * 1024},
	{Name: "stdio_arguments_bytes", Maximum: 32 * 1024},
	{Name: "stdio_environment_name_bytes", Maximum: 4 * 1024},
	{Name: "stdio_environment_value_bytes", Maximum: 4 * 1024},
	{Name: "secret_slot_name_bytes", Maximum: 64},
	{Name: "resource_url_bytes", Maximum: 8 * 1024},
	{Name: "server_identities", Maximum: 1024},
	{Name: "servers", Maximum: 64},
	{Name: "enabled_servers", Maximum: 32},
	{Name: "downstream_runtimes", Maximum: 32},
	{Name: "server_reconciliations", Maximum: 4},
	{Name: "per_server_reconciliation", Maximum: 1},
	{Name: "catalog_traversals", Maximum: 4},
	{Name: "oauth_flows", Maximum: 16},
	{Name: "per_server_oauth_flows", Maximum: 1},
	{Name: "oauth_callback_work", Maximum: 8},
	{Name: "terminal_operations", Maximum: 64},
	{Name: "terminal_auth_flows", Maximum: 16},
	{Name: "s2_list_page", Maximum: 100},
	{Name: "active_tools_per_server", Maximum: 256},
	{Name: "active_tools", Maximum: 2048},
	{Name: "durable_tool_identities_per_server", Maximum: 512},
	{Name: "durable_tool_identities", Maximum: 4096},
	{Name: "tools_list_pages", Maximum: 32},
	{Name: "tools_list_page_bytes", Maximum: 4 * 1024 * 1024},
	{Name: "tool_descriptor_bytes", Maximum: 128 * 1024},
	{Name: "tool_schema_bytes", Maximum: 96 * 1024},
	{Name: "tool_name_bytes", Maximum: 128},
	{Name: "external_tool_name_bytes", Maximum: 128},
	{Name: "tool_title_bytes", Maximum: 1024},
	{Name: "tool_description_bytes", Maximum: 16 * 1024},
	{Name: "downstream_mcp_body_bytes", Maximum: 4 * 1024 * 1024},
	{Name: "downstream_sse_event_bytes", Maximum: 4 * 1024 * 1024},
	{Name: "downstream_legacy_session_id_bytes", Maximum: 512},
	{Name: "oauth_metadata_body_bytes", Maximum: 1024 * 1024},
	{Name: "oauth_json_depth", Maximum: 64},
	{Name: "oauth_response_body_bytes", Maximum: 256 * 1024},
	{Name: "oauth_url_bytes", Maximum: 8 * 1024},
	{Name: "oauth_query_bytes", Maximum: 8 * 1024},
	{Name: "oauth_client_id_bytes", Maximum: 8 * 1024},
	{Name: "oauth_client_secret_bytes", Maximum: 8 * 1024},
	{Name: "oauth_scope_count", Maximum: 64},
	{Name: "oauth_scope_token_bytes", Maximum: 256},
	{Name: "oauth_scope_bytes", Maximum: 8 * 1024},
	{Name: "stdio_protocol_frame_bytes", Maximum: 4 * 1024 * 1024},
	{Name: "stdio_stderr_bytes", Maximum: 64 * 1024},
	{Name: "stdio_output_rate_bytes_per_second", Maximum: 8 * 1024 * 1024},
	{Name: "stdio_output_burst_bytes", Maximum: 8 * 1024 * 1024},
	{Name: "downstream_dispatch", Maximum: 32},
	{Name: "per_server_downstream_dispatch", Maximum: 4},
	{Name: "s2_idempotency_records", Maximum: 1024},
}

func FixedLimits() []FixedLimit {
	return append([]FixedLimit(nil), fixedLimits...)
}

func FixedLimitByName(name string) (FixedLimit, bool) {
	for _, limit := range fixedLimits {
		if limit.Name == name {
			return limit, true
		}
	}
	return FixedLimit{}, false
}

func (limit FixedLimit) Allows(value int64) bool {
	return value >= 0 && value <= limit.Maximum
}

var reconciliationRetryDelays = []time.Duration{
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
}

func ReconciliationRetryDelays() []time.Duration {
	return append([]time.Duration(nil), reconciliationRetryDelays...)
}
