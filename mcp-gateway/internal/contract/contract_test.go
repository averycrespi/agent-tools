package contract

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRoutesMatchTheS1Contract(t *testing.T) {
	t.Parallel()

	expected := []Route{
		{Pattern: "/", Methods: []string{"GET"}, Authority: AuthorityPublic},
		{Pattern: "/assets/*", Methods: []string{"GET"}, Authority: AuthorityPublic},
		{Pattern: "/livez", Methods: []string{"GET"}, Authority: AuthorityPublic},
		{Pattern: "/readyz", Methods: []string{"GET"}, Authority: AuthorityPublic},
		{Pattern: "/mcp", Methods: []string{"DELETE", "GET", "POST"}, Authority: AuthorityAgent},
		{Pattern: "/oauth/callback", Methods: []string{"GET"}, Authority: AuthorityOAuthState},
		{Pattern: "/api/v1/admin-sessions", Methods: []string{"POST"}, Authority: AuthorityAdminBearer},
		{Pattern: "/api/v1/admin-sessions/current", Methods: []string{"DELETE", "POST"}, Authority: AuthorityAdminSession},
		{Pattern: "/api/v1/admin-credentials", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/admin-credentials/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/system-status", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/backups", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/backups/{id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/events", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	}

	require.Equal(t, expected, Routes()[:len(expected)], "S1 routes must remain the table prefix")
	for _, route := range expected {
		require.Equal(t, joinMethods(route.Methods), route.Allow())
	}

	routes := Routes()
	routes[0].Methods[0] = "POST"
	routes[0].Pattern = "/changed"
	require.Equal(t, expected, Routes()[:len(expected)], "callers must not be able to mutate the canonical S1 table")
}

func TestRouteForPathClassifiesOnlyOwnedPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"/":                              "/",
		"/assets/app.js":                 "/assets/*",
		"/livez":                         "/livez",
		"/readyz":                        "/readyz",
		"/mcp":                           "/mcp",
		"/oauth/callback":                "/oauth/callback",
		"/api/v1/admin-sessions":         "/api/v1/admin-sessions",
		"/api/v1/admin-sessions/current": "/api/v1/admin-sessions/current",
		"/api/v1/admin-credentials":      "/api/v1/admin-credentials",
		"/api/v1/admin-credentials/01ARZ3NDEKTSV4RRFFQ69G5FAV": "/api/v1/admin-credentials/{id}",
		"/api/v1/system-status":                                "/api/v1/system-status",
		"/api/v1/backups":                                      "/api/v1/backups",
		"/api/v1/backups/01ARZ3NDEKTSV4RRFFQ69G5FAV":           "/api/v1/backups/{id}",
		"/api/v1/events":                                       "/api/v1/events",
	}
	for path, pattern := range tests {
		route, ok := RouteForPath(path)
		require.True(t, ok, path)
		require.Equal(t, pattern, route.Pattern, path)
	}

	for _, path := range []string{
		"", "mcp", "/assets", "/assets/", "/unknown", "/api/v1/admin-credentials/", "/api/v1/admin-credentials/a/b", "/api/v1/backups/a/b", "/mcp/",
	} {
		_, ok := RouteForPath(path)
		require.False(t, ok, path)
	}
}

func TestProblemsMatchTheSafeEnvelopeContract(t *testing.T) {
	t.Parallel()

	expected := []Problem{
		{Status: 400, Code: ProblemMalformedRequest, Title: "The request is invalid."},
		{Status: 400, Code: ProblemInvalidJSON, Title: "The JSON body is invalid."},
		{Status: 400, Code: ProblemInvalidCursor, Title: "The cursor is invalid."},
		{Status: 400, Code: ProblemInvalidIdempotencyKey, Title: "The idempotency key is invalid."},
		{Status: 400, Code: ProblemAmbiguousCredentials, Title: "Multiple credential types were supplied."},
		{Status: 400, Code: ProblemInvalidOAuthState, Title: "The OAuth state is invalid or expired."},
		{Status: 401, Code: ProblemAuthenticationRequired, Title: "Authentication is required."},
		{Status: 403, Code: ProblemCredentialDomainMismatch, Title: "The credential is for a different authority."},
		{Status: 403, Code: ProblemForbiddenOrigin, Title: "The Origin is not accepted."},
		{Status: 403, Code: ProblemCSRFFailed, Title: "CSRF validation failed."},
		{Status: 404, Code: ProblemNotFound, Title: "The resource was not found."},
		{Status: 405, Code: ProblemMethodNotAllowed, Title: "The method is not allowed."},
		{Status: 409, Code: ProblemConflict, Title: "The request conflicts with current state."},
		{Status: 409, Code: ProblemIdempotencyConflict, Title: "The idempotency key conflicts with prior work."},
		{Status: 413, Code: ProblemBodyTooLarge, Title: "The request body is too large."},
		{Status: 415, Code: ProblemUnsupportedMediaType, Title: "The media type is not supported."},
		{Status: 421, Code: ProblemMisdirectedRequest, Title: "The Host is not accepted."},
		{Status: 429, Code: ProblemResourceLimit, Title: "The resource limit is reached."},
		{Status: 503, Code: ProblemStorageUnavailable, Title: "Storage is unavailable."},
		{Status: 503, Code: ProblemKeyringUnavailable, Title: "The credential provider is unavailable."},
		{Status: 503, Code: ProblemShuttingDown, Title: "The service is shutting down."},
	}

	require.Equal(t, expected, Problems()[:len(expected)], "S1 problems must remain the table prefix")
	for _, problem := range expected {
		actual, ok := ProblemForCode(problem.Code)
		require.True(t, ok)
		require.Equal(t, problem, actual)
	}
	_, ok := ProblemForCode("dependency_error")
	require.False(t, ok)
}

func TestFixedLimitsAcceptNAndRejectNPlusOne(t *testing.T) {
	t.Parallel()

	expected := map[string]int64{
		"request_target_bytes":         8 * 1024,
		"request_header_bytes":         32 * 1024,
		"request_header_count":         100,
		"request_header_value_bytes":   8 * 1024,
		"api_json_body_bytes":          1 * 1024 * 1024,
		"mcp_body_bytes":               4 * 1024 * 1024,
		"json_depth":                   64,
		"http_regular":                 128,
		"http_control_auth":            32,
		"http_admin":                   16,
		"http_health":                  8,
		"mcp_work":                     32,
		"mcp_streams":                  32,
		"admin_sessions":               128,
		"legacy_sessions":              128,
		"event_streams":                16,
		"event_buffered_invalidations": 16,
		"backup_work":                  1,
		"backup_records":               64,
		"admin_credentials":            128,
		"admin_list_page":              100,
		"backup_list_page":             100,
		"database_bytes":               1 * 1024 * 1024 * 1024,
		"idempotency_key_bytes":        128,
		"idempotency_records":          1024,
		"opaque_id_bytes":              26,
		"cursor_bytes":                 512,
		"sse_frame_bytes":              512,
		"keyring_secret_bytes":         256 * 1024,
		"keyring_chunk_bytes":          3000,
		"keyring_candidates":           64,
		"keyring_work":                 1,
	}

	limits := FixedLimits()
	require.GreaterOrEqual(t, len(limits), len(expected))
	for _, limit := range limits[:len(expected)] {
		maximum, ok := expected[limit.Name]
		require.True(t, ok, limit.Name)
		require.Equal(t, maximum, limit.Maximum, limit.Name)
		require.True(t, limit.Allows(maximum), limit.Name)
		require.False(t, limit.Allows(maximum+1), limit.Name)
		require.False(t, limit.Allows(-1), limit.Name)
		lookedUp, ok := FixedLimitByName(limit.Name)
		require.True(t, ok, limit.Name)
		require.Equal(t, limit, lookedUp, limit.Name)
	}
	_, ok := FixedLimitByName("unknown")
	require.False(t, ok)
}

func TestFixedDurationsAndRangesMatchTheS1Contract(t *testing.T) {
	t.Parallel()

	require.Equal(t, 5*time.Second, HeaderReadDeadline)
	require.Equal(t, 30*time.Second, APIHandlerDeadline)
	require.Equal(t, 2*time.Second, SQLiteBusyDeadline)
	require.Equal(t, 15*time.Second, SSEKeepaliveInterval)
	require.Equal(t, 15*time.Second, SSEBlockedWriteDeadline)
	require.Equal(t, 30*time.Minute, LegacyIdleLifetime)
	require.Equal(t, 8*time.Hour, LegacyAbsoluteLifetime)
	require.Equal(t, 10*time.Second, GracefulShutdownDeadline)
	require.Equal(t, 24*time.Hour, IdempotencyRetention)
	require.Equal(t, 5*time.Minute, CredentialMinimumLifetime)
	require.Equal(t, 365*24*time.Hour, CredentialMaximumLifetime)
	require.Equal(t, 50, AdminListPageDefault)
	require.Equal(t, 50, BackupListPageDefault)
	require.Equal(t, 1, IdempotencyKeyMinimumBytes)
}

func TestResourceMechanicsAreTargeted(t *testing.T) {
	t.Parallel()

	mechanics := ResourceMechanics()
	require.GreaterOrEqual(t, len(mechanics), 12)

	var cursored, idempotent int
	for _, mechanic := range mechanics[:12] {
		if mechanic.Cursor {
			cursored++
			require.Equal(t, "GET", mechanic.Method)
			require.Contains(t, []string{"/api/v1/admin-credentials", "/api/v1/backups"}, mechanic.Pattern)
		}
		if mechanic.Idempotency {
			idempotent++
			require.Equal(t, "POST", mechanic.Method)
			require.Equal(t, "/api/v1/backups", mechanic.Pattern)
		}
		require.False(t, mechanic.ETag)
		require.False(t, mechanic.EventReplay)
	}
	require.Equal(t, 2, cursored)
	require.Equal(t, 1, idempotent)
}

func TestMediaTypesAndApprovedSecretSinksAreClosed(t *testing.T) {
	t.Parallel()

	require.Equal(t, "application/json", MediaTypeJSON)
	require.Equal(t, "application/problem+json", MediaTypeProblemJSON)
	require.Equal(t, "text/event-stream", MediaTypeEventStream)
	require.Equal(t, []SecretSink{SecretSinkControllingTerminal, SecretSinkOwnerOnlyFile}, ApprovedSecretSinks()[:2], "S1 sinks must remain the table prefix")
	require.Equal(t, uint32(0o600), uint32(SecretOutputFileMode))
	require.Equal(t, "\n", SecretOutputTerminator)
	require.NotContains(t, ApprovedSecretSinks(), SecretSink("stdout"))
	require.NotContains(t, ApprovedSecretSinks(), SecretSink("stderr"))
}

func TestSafeResourceJSONShapesAreExact(t *testing.T) {
	t.Parallel()

	credential := AdminCredential{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Fingerprint: "0123456789abcdef", CreatedAt: "2026-08-22T00:00:00Z",
		ExpiresAt: nil, NonExpiring: true, Status: "active", Revision: "1",
	}
	backup := Backup{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", CreatedAt: "2026-08-22T00:00:00Z", InstallationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		SchemaVersion: "1", SourceRevision: "1", SizeBytes: 1, SHA256: "digest",
	}
	status := SystemStatus{
		Process:   ProcessStatus{State: "ready", Ready: true, StartedAt: "2026-08-22T00:00:00Z"},
		SQLite:    SQLiteStatus{State: "ready", SchemaVersion: "1", Revision: "1", Latched: false},
		Keyring:   KeyringStatus{Capability: "ready"},
		Limits:    LimitsStatus{},
		Backup:    BackupStatus{State: "idle", LastCompletedAt: nil},
		Protocols: ProtocolStatus{Modern: ModernProtocolVersion, Legacy: LegacyProtocolVersion, AgentAuth: "deny_all"},
	}

	requireJSONKeys(t, credential, "id", "fingerprint", "created_at", "expires_at", "non_expiring", "status", "revision")
	requireJSONKeys(t, CreatedAdminCredential{AdminCredential: credential, Bearer: "one-time"}, "id", "fingerprint", "created_at", "expires_at", "non_expiring", "status", "revision", "bearer")
	requireJSONKeys(t, backup, "id", "created_at", "installation_id", "schema_version", "source_revision", "size_bytes", "sha256")
	requireJSONKeys(t, status, "process", "sqlite", "keyring", "limits", "backup", "protocols")
	requireJSONKeys(t, status.Limits,
		"http_regular", "http_control_auth", "http_admin", "http_health", "mcp_work", "mcp_streams", "admin_sessions", "legacy_sessions",
		"event_streams", "backup_work", "backup_records", "admin_credentials", "idempotency_records", "keyring_candidates", "keyring_work", "database_bytes",
		"server_identities", "servers", "downstream_runtimes", "server_reconciliations", "catalog_traversals", "oauth_flows", "oauth_callback_work",
		"s2_idempotency_records", "active_tools", "durable_tool_identities", "downstream_dispatch", "principals", "grants",
		"grant_requests", "grant_request_evidence_bytes",
	)
	requireJSONKeys(t, ProblemEnvelope{Status: 400, Code: ProblemMalformedRequest, Title: "The request is invalid."}, "status", "code", "title")
}

func requireJSONKeys(t *testing.T, value any, expected ...string) {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &object))
	require.ElementsMatch(t, expected, mapKeys(object))
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
