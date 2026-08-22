package contract

import "io/fs"

const (
	MediaTypeJSON        = "application/json"
	MediaTypeProblemJSON = "application/problem+json"
	MediaTypeEventStream = "text/event-stream"

	ModernProtocolVersion = "2026-07-28"
	LegacyProtocolVersion = "2025-11-25"

	AdminBearerPrefix = "mgw_admin_"
	AgentBearerPrefix = "mgw_agent_"
	SessionCookieName = "mcp_gateway_session"
)

type ResourceMechanic struct {
	Pattern     string
	Method      string
	Cursor      bool
	Idempotency bool
	ETag        bool
	EventReplay bool
}

var resourceMechanics = []ResourceMechanic{
	{Pattern: "/api/v1/admin-sessions", Method: "POST"},
	{Pattern: "/api/v1/admin-sessions/current", Method: "DELETE"},
	{Pattern: "/api/v1/admin-credentials", Method: "GET", Cursor: true},
	{Pattern: "/api/v1/admin-credentials", Method: "POST"},
	{Pattern: "/api/v1/admin-credentials/{id}", Method: "DELETE"},
	{Pattern: "/api/v1/admin-credentials/{id}", Method: "GET"},
	{Pattern: "/api/v1/system-status", Method: "GET"},
	{Pattern: "/api/v1/backups", Method: "GET", Cursor: true},
	{Pattern: "/api/v1/backups", Method: "POST", Idempotency: true},
	{Pattern: "/api/v1/backups/{id}", Method: "DELETE"},
	{Pattern: "/api/v1/backups/{id}", Method: "GET"},
	{Pattern: "/api/v1/events", Method: "GET"},
}

func ResourceMechanics() []ResourceMechanic {
	return append([]ResourceMechanic(nil), resourceMechanics...)
}

type SecretSink string

const (
	SecretSinkControllingTerminal SecretSink  = "controlling_terminal"
	SecretSinkOwnerOnlyFile       SecretSink  = "owner_only_file"
	SecretOutputFileMode          fs.FileMode = 0o600
	SecretOutputTerminator                    = "\n"
)

func ApprovedSecretSinks() []SecretSink {
	return []SecretSink{SecretSinkControllingTerminal, SecretSinkOwnerOnlyFile}
}
