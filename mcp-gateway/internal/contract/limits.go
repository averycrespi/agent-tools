package contract

import "time"

const (
	AdminListPageDefault       = 50
	BackupListPageDefault      = 50
	IdempotencyKeyMinimumBytes = 1
)

const (
	HeaderReadDeadline           = 5 * time.Second
	APIHandlerDeadline           = 30 * time.Second
	SQLiteBusyDeadline           = 2 * time.Second
	SSEKeepaliveInterval         = 15 * time.Second
	SSEBlockedWriteDeadline      = 15 * time.Second
	AdminSessionIdleLifetime     = 30 * time.Minute
	AdminSessionAbsoluteLifetime = 8 * time.Hour
	LegacyIdleLifetime           = 30 * time.Minute
	LegacyAbsoluteLifetime       = 8 * time.Hour
	GracefulShutdownDeadline     = 10 * time.Second
	IdempotencyRetention         = 24 * time.Hour
	CredentialMinimumLifetime    = 5 * time.Minute
	CredentialMaximumLifetime    = 365 * 24 * time.Hour
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
