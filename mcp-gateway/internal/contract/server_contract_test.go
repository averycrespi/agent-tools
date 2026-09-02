package contract

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerRoutesAndMechanicsAreExact(t *testing.T) {
	t.Parallel()

	expectedRoutes := []Route{
		{Pattern: "/api/v1/servers", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}", Methods: []string{"DELETE", "GET", "PATCH"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/operations", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/operations/{operation_id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/credential-replacements", Methods: []string{"POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/auth-flows", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/auth-flows/{flow_id}", Methods: []string{"DELETE", "GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/catalog", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/descriptors", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/servers/{id}/descriptors/{tool_id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
	}
	routes := Routes()
	require.Equal(t, expectedRoutes, routes[14:14+len(expectedRoutes)], "S2 routes must remain after the S1 prefix")

	paths := map[string]string{
		"/api/v1/servers": "/api/v1/servers",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV":                                       "/api/v1/servers/{id}",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/operations":                            "/api/v1/servers/{id}/operations",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/operations/01ARZ3NDEKTSV4RRFFQ69G5FAA": "/api/v1/servers/{id}/operations/{operation_id}",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/credential-replacements":               "/api/v1/servers/{id}/credential-replacements",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/auth-flows":                            "/api/v1/servers/{id}/auth-flows",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/auth-flows/01ARZ3NDEKTSV4RRFFQ69G5FAB": "/api/v1/servers/{id}/auth-flows/{flow_id}",
		"/api/v1/catalog": "/api/v1/catalog",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/descriptors":                            "/api/v1/servers/{id}/descriptors",
		"/api/v1/servers/01ARZ3NDEKTSV4RRFFQ69G5FAV/descriptors/01ARZ3NDEKTSV4RRFFQ69G5FAC": "/api/v1/servers/{id}/descriptors/{tool_id}",
	}
	for path, pattern := range paths {
		route, ok := RouteForPath(path)
		require.True(t, ok, path)
		require.Equal(t, pattern, route.Pattern, path)
	}
	for _, path := range []string{
		"/api/v1/servers/", "/api/v1/servers/a/b", "/api/v1/servers/a/operations/", "/api/v1/servers/a/operations/b/c",
		"/api/v1/servers/a/auth-flows/", "/api/v1/servers/a/descriptors/b/c", "/api/v1/catalog/extra",
	} {
		_, ok := RouteForPath(path)
		require.False(t, ok, path)
	}

	expectedMechanics := []ResourceMechanic{
		{Pattern: "/api/v1/servers", Method: "GET", RequestSchema: "ServerListQuery", SuccessSchema: "Page<Server>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/servers", Method: "POST", RequestSchema: "ServerCreate", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200, 201}, Idempotency: true, ETag: true},
		{Pattern: "/api/v1/servers/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Server", SuccessStatuses: []int{200}, ETag: true},
		{Pattern: "/api/v1/servers/{id}", Method: "PATCH", RequestSchema: "ServerPatch", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/servers/{id}", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200, 202}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/servers/{id}/operations", Method: "GET", RequestSchema: "ServerOperationListQuery", SuccessSchema: "Page<ServerOperation>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/servers/{id}/operations", Method: "POST", RequestSchema: "ServerOperationCreate", SuccessSchema: "ServerOperationMutation", SuccessStatuses: []int{200, 202}, Idempotency: true, Precondition: true},
		{Pattern: "/api/v1/servers/{id}/operations/{operation_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ServerOperation", SuccessStatuses: []int{200}},
		{Pattern: "/api/v1/servers/{id}/credential-replacements", Method: "POST", RequestSchema: "CredentialReplacement", SuccessSchema: "CredentialReplacementResult", SuccessStatuses: []int{202}, Precondition: true},
		{Pattern: "/api/v1/servers/{id}/auth-flows", Method: "GET", RequestSchema: "ServerAuthFlowListQuery", SuccessSchema: "Page<ServerAuthFlow>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/servers/{id}/auth-flows", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "AuthFlowCreation", SuccessStatuses: []int{201}, Precondition: true},
		{Pattern: "/api/v1/servers/{id}/auth-flows/{flow_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ServerAuthFlow", SuccessStatuses: []int{200}},
		{Pattern: "/api/v1/servers/{id}/auth-flows/{flow_id}", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "Empty", SuccessStatuses: []int{204}},
		{Pattern: "/api/v1/catalog", Method: "GET", RequestSchema: "CatalogListQuery", SuccessSchema: "CatalogPage", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/servers/{id}/descriptors", Method: "GET", RequestSchema: "DescriptorListQuery", SuccessSchema: "Page<ToolDescriptor>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/servers/{id}/descriptors/{tool_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ToolDescriptor", SuccessStatuses: []int{200}},
		{Pattern: "/oauth/callback", Method: "GET", RequestSchema: "OAuthCallbackQuery", SuccessSchema: "OAuthCallbackHTML", SuccessStatuses: []int{200, 400, 503}},
	}
	mechanics := ResourceMechanics()
	require.Equal(t, expectedMechanics, mechanics[12:12+len(expectedMechanics)], "S2 mechanics must remain after the S1 prefix")
}

func TestServerProblemsAreExact(t *testing.T) {
	t.Parallel()

	expected := []Problem{
		{Status: 400, Code: ProblemInvalidServerConfiguration, Title: "The server configuration is invalid."},
		{Status: 400, Code: ProblemInvalidOperation, Title: "The server operation is invalid."},
		{Status: 409, Code: ProblemNamespaceUnavailable, Title: "The server namespace is unavailable."},
		{Status: 409, Code: ProblemOperationConflict, Title: "The server has conflicting work."},
		{Status: 409, Code: ProblemOAuthFlowActive, Title: "The OAuth flow is already exchanging."},
		{Status: 409, Code: ProblemStaleCursor, Title: "The cursor snapshot is no longer available."},
		{Status: 412, Code: ProblemStaleRevision, Title: "The server revision is stale."},
		{Status: 428, Code: ProblemPreconditionRequired, Title: "The current server revision is required."},
		{Status: 503, Code: ProblemDownstreamUnavailable, Title: "The downstream server is unavailable."},
	}
	problems := Problems()
	require.Equal(t, expected, problems[21:21+len(expected)], "S2 problems must remain after the S1 prefix")
}

func TestServerClosedVocabulariesRejectUnknownValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid []string
		parse func(string) error
	}{
		{"transport kind", []string{"stdio", "streamable_http"}, func(value string) error { _, err := ParseTransportKind(value); return err }},
		{"protocol mode", []string{"auto", "modern", "legacy"}, func(value string) error { _, err := ParseProtocolMode(value); return err }},
		{"authentication mode", []string{"none", "bearer", "oauth"}, func(value string) error { _, err := ParseAuthenticationMode(value); return err }},
		{"registration mode", []string{"static", "dynamic"}, func(value string) error { _, err := ParseRegistrationMode(value); return err }},
		{"token endpoint auth method", []string{"none", "client_secret_basic", "client_secret_post"}, func(value string) error { _, err := ParseTokenEndpointAuthMethod(value); return err }},
		{"desired state", []string{"enabled", "disabled", "deleted"}, func(value string) error { _, err := ParseDesiredServerState(value); return err }},
		{"runtime state", []string{"inactive", "activating", "active", "stopping", "retry_wait", "degraded", "authentication_required", "deleted"}, func(value string) error { _, err := ParseRuntimeState(value); return err }},
		{"operation kind", []string{"activate", "reload", "retry", "refresh_catalog", "credential_replace", "disable", "delete", "disconnect_credentials"}, func(value string) error { _, err := ParseServerOperationKind(value); return err }},
		{"operation state", []string{"scheduled", "running", "succeeded", "failed", "cancelled", "superseded", "interrupted"}, func(value string) error { _, err := ParseServerOperationState(value); return err }},
		{"credential state", []string{"not_required", "ready", "absent", "locked", "interaction_required", "unavailable", "unsupported", "refreshing", "reauthentication_required", "disconnecting", "cleanup_pending"}, func(value string) error { _, err := ParseServerCredentialState(value); return err }},
		{"durable catalog state", []string{"empty", "current", "stale", "unavailable", "retired"}, func(value string) error { _, err := ParseDurableCatalogState(value); return err }},
		{"active catalog state", []string{"absent", "refreshing", "current", "stale", "unavailable"}, func(value string) error { _, err := ParseActiveCatalogState(value); return err }},
		{"aggregate catalog state", []string{"empty", "current", "degraded"}, func(value string) error { _, err := ParseAggregateCatalogState(value); return err }},
		{"flow state", []string{"preparing", "awaiting_callback", "exchanging", "succeeded", "failed", "expired", "cancelled", "superseded", "interrupted"}, func(value string) error { _, err := ParseAuthFlowState(value); return err }},
		{"OAuth diagnostic stage", []string{"metadata_discovery", "client_registration", "authorization_request", "callback_validation", "token_exchange", "credential_installation"}, func(value string) error { _, err := ParseOAuthDiagnosticStage(value); return err }},
		{"credential kind", []string{"static_credential", "oauth_client", "oauth_tokens"}, func(value string) error { _, err := ParseServerCredentialKind(value); return err }},
		{"descriptor retired filter", []string{"include", "exclude", "only"}, func(value string) error { _, err := ParseRetiredFilter(value); return err }},
		{"reason", []string{"configuration_invalid", "resource_limit", "connectivity", "tls_failed", "protocol_unsupported", "protocol_invalid", "authentication_rejected", "credential_absent", "keyring_absent", "keyring_locked", "keyring_interaction_required", "keyring_unavailable", "keyring_unsupported", "oauth_rejected", "oauth_expired", "registration_expired", "process_exited", "output_limit", "stop_unconfirmed", "catalog_invalid", "catalog_limit", "catalog_stale", "superseded", "cancelled", "interrupted", "revocation_failed", "revocation_unsupported", "cleanup_pending"}, func(value string) error { _, err := ParsePublicReason(value); return err }},
	}
	require.Equal(t, []ServerOperationKind{OperationReload, OperationRetry, OperationRefreshCatalog, OperationDisconnectCredentials}, ExplicitServerOperationKinds())
	for _, value := range []string{"include", "exclude", "only"} {
		parsed, err := ParseDescriptorRetiredFilter(value)
		require.NoError(t, err)
		require.Equal(t, value, string(parsed))
	}
	for _, value := range []string{"reload", "retry", "refresh_catalog", "disconnect_credentials"} {
		_, err := ParseExplicitServerOperationKind(value)
		require.NoError(t, err, value)
	}
	_, err := ParseExplicitServerOperationKind("activate")
	require.Error(t, err, "internally generated operation kinds are not API inputs")

	require.Equal(t, []ServerCredentialKind{ServerCredentialStatic, ServerCredentialOAuthClient}, CredentialReplacementKinds())
	for _, value := range []string{"static_credential", "oauth_client"} {
		_, err := ParseCredentialReplacementKind(value)
		require.NoError(t, err, value)
	}
	_, err = ParseCredentialReplacementKind("oauth_tokens")
	require.Error(t, err, "OAuth tokens are installed only through validated OAuth responses")

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, value := range test.valid {
				require.NoError(t, test.parse(value), value)
			}
			require.Error(t, test.parse("unknown"))
		})
	}
}

func TestServerLimitsAndDeadlinesAreExact(t *testing.T) {
	t.Parallel()

	expected := map[string]int64{
		"namespace_bytes": 32, "display_name_bytes": 256, "stdio_arguments": 64, "stdio_environment_entries": 32,
		"stdio_secret_environment_entries": 16, "stdio_path_bytes": 4096, "stdio_argument_bytes": 4096, "stdio_arguments_bytes": 32768,
		"stdio_environment_name_bytes": 4096, "stdio_environment_value_bytes": 4096, "secret_slot_name_bytes": 64, "resource_url_bytes": 8192,
		"server_identities": 1024, "servers": 64, "enabled_servers": 32, "downstream_runtimes": 32,
		"server_reconciliations": 4, "per_server_reconciliation": 1, "catalog_traversals": 4, "oauth_flows": 16,
		"per_server_oauth_flows": 1, "oauth_callback_work": 8, "terminal_operations": 64, "terminal_auth_flows": 16,
		"s2_list_page": 100, "active_tools_per_server": 256, "active_tools": 2048, "durable_tool_identities_per_server": 512,
		"durable_tool_identities": 4096, "tools_list_pages": 32, "tools_list_page_bytes": 4 * 1024 * 1024, "tool_descriptor_bytes": 128 * 1024,
		"tool_schema_bytes": 96 * 1024, "tool_name_bytes": 128, "external_tool_name_bytes": 128, "tool_title_bytes": 1024,
		"tool_description_bytes": 16 * 1024, "downstream_mcp_body_bytes": 4 * 1024 * 1024, "downstream_sse_event_bytes": 4 * 1024 * 1024,
		"downstream_legacy_session_id_bytes": 512, "oauth_metadata_body_bytes": 1024 * 1024, "oauth_json_depth": 64,
		"oauth_response_body_bytes": 256 * 1024, "oauth_url_bytes": 8 * 1024, "oauth_query_bytes": 8 * 1024,
		"oauth_client_id_bytes": 8 * 1024, "oauth_client_secret_bytes": 8 * 1024, "oauth_scope_count": 64, "oauth_scope_token_bytes": 256,
		"oauth_scope_bytes": 8 * 1024, "stdio_protocol_frame_bytes": 4 * 1024 * 1024, "stdio_stderr_bytes": 64 * 1024,
		"stdio_output_rate_bytes_per_second": 8 * 1024 * 1024, "stdio_output_burst_bytes": 8 * 1024 * 1024,
		"downstream_dispatch": 32, "per_server_downstream_dispatch": 4, "s2_idempotency_records": 1024,
	}
	require.GreaterOrEqual(t, len(FixedLimits()), 32+len(expected), "later slices may only append fixed limits")
	for name, maximum := range map[string]int64{"request_header_bytes": 32 * 1024, "request_header_count": 100, "request_header_value_bytes": 8 * 1024} {
		limit, ok := FixedLimitByName(name)
		require.True(t, ok, name)
		require.Equal(t, maximum, limit.Maximum, "downstream HTTP reuses the S1 header bound")
	}
	for name, maximum := range expected {
		limit, ok := FixedLimitByName(name)
		require.True(t, ok, name)
		require.Equal(t, maximum, limit.Maximum, name)
		require.True(t, limit.Allows(maximum), name)
		require.False(t, limit.Allows(maximum+1), name)
	}

	require.Equal(t, 50, S2ListPageDefault)
	require.Equal(t, 5*time.Minute, OAuthFlowLifetime)
	require.Equal(t, 10*time.Second, DownstreamConnectDeadline)
	require.Equal(t, 15*time.Second, OAuthRequestDeadline)
	require.Equal(t, 30*time.Second, DownstreamInitializationDeadline)
	require.Equal(t, 15*time.Second, CatalogPageDeadline)
	require.Equal(t, 60*time.Second, CatalogTraversalDeadline)
	require.Equal(t, 60*time.Second, MaximumDownstreamCallDeadline)
	require.Equal(t, 3*time.Second, StdioGracefulStopDeadline)
	require.Equal(t, 2*time.Second, StdioForcedStopDeadline)
	require.Equal(t, 5*time.Minute, CatalogPollInterval)
	require.Equal(t, 30*time.Second, CatalogPollMaximumJitter)
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second}, ReconciliationRetryDelays())
}

func TestServerEventsSecretSinksETagsAndStatusOccupancies(t *testing.T) {
	t.Parallel()

	expectedInvalidations := []InvalidationKind{
		InvalidationAdminCredentials, InvalidationSystemStatus, InvalidationBackups, InvalidationServers,
		InvalidationServerOperations, InvalidationServerAuthFlows, InvalidationCatalog,
	}
	require.Equal(t, expectedInvalidations, InvalidationKinds()[:len(expectedInvalidations)], "S2 invalidations must remain the table prefix")
	expectedSinks := []SecretSink{
		SecretSinkControllingTerminal, SecretSinkOwnerOnlyFile, SecretSinkAdminCredentialReplacement,
		SecretSinkDCRClientSecret, SecretSinkAuthorizationCodeTokenResponse, SecretSinkRefreshResponse,
		SecretSinkAuthoritativeGenerationRefreshCopy,
	}
	require.Equal(t, expectedSinks, ApprovedSecretSinks()[:len(expectedSinks)], "S2 sinks must remain the table prefix")

	etag := ServerETag("01ARZ3NDEKTSV4RRFFQ69G5FAV", "7")
	require.Equal(t, `"server-01ARZ3NDEKTSV4RRFFQ69G5FAV-7"`, etag)
	require.True(t, MatchesServerETag(etag, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "7"))
	for _, invalid := range []string{"", "*", `W/` + etag, etag + ", " + etag, `"server-01ARZ3NDEKTSV4RRFFQ69G5FAV-8"`} {
		require.False(t, MatchesServerETag(invalid, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "7"), invalid)
	}

	encoded, err := json.Marshal(LimitsStatus{})
	require.NoError(t, err)
	var limits map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &limits))
	for _, name := range []string{"server_identities", "servers", "downstream_runtimes", "server_reconciliations", "catalog_traversals", "oauth_flows", "oauth_callback_work", "s2_idempotency_records", "active_tools", "durable_tool_identities", "downstream_dispatch"} {
		require.Contains(t, limits, name)
	}
}

func TestServerResourceShapesAreExact(t *testing.T) {
	t.Parallel()

	stdio := StdioTransport{Kind: TransportStdio, Executable: "/bin/server", Arguments: []string{"--stdio"}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}
	server := Server{
		ID: "server", Namespace: "example", DisplayName: "Example", DesiredState: DesiredServerEnabled, DesiredRevision: "1", Transport: stdio,
		CredentialRevisions: CredentialRevisions{StaticCredential: "0", OAuthClient: "0", OAuthTokens: "0"}, CredentialState: ServerCredentialNotRequired,
		Runtime:   ServerRuntime{State: RuntimeInactive, Reconciliation: LimitStatus{}, Dispatch: LimitStatus{}},
		Catalog:   ServerCatalog{DurableState: DurableCatalogEmpty, ActiveState: ActiveCatalogAbsent, DurableToolCount: 0, ActiveToolCount: 0, Traversal: LimitStatus{}},
		CreatedAt: "2026-08-22T00:00:00Z", UpdatedAt: "2026-08-22T00:00:00Z",
	}
	requireJSONKeys(t, server, "id", "namespace", "display_name", "desired_state", "desired_revision", "transport", "credential_revisions", "credential_state", "runtime", "catalog", "created_at", "updated_at", "deleted_at")
	requireJSONKeys(t, stdio, "kind", "executable", "arguments", "working_directory", "environment", "secret_environment")
	requireJSONKeys(t, StreamableHTTPTransport{}, "kind", "url", "protocol_mode", "authentication")
	requireJSONKeys(t, OAuthAuthentication{}, "mode", "registration", "trusted_origins", "request_offline_access")
	requireJSONKeys(t, StaticOAuthRegistration{}, "mode", "issuer", "client_id", "token_endpoint_auth_method")
	requireJSONKeys(t, DynamicOAuthRegistration{}, "mode", "issuer")
	requireJSONKeys(t, ServerOperation{}, "id", "server_id", "kind", "target_desired_revision", "target_credential_revisions", "state", "reason", "created_at", "started_at", "finished_at")
	requireJSONKeys(t, ServerAuthFlow{}, "id", "server_id", "flow_state", "target_desired_revision", "registration_revision", "created_at", "expires_at", "finished_at", "reason", "diagnostic")
	requireJSONKeys(t, OAuthDiagnostic{}, "correlation_id", "stage", "reason", "http_status")
	requireJSONKeys(t, ToolDescriptor{}, "id", "server_id", "upstream_name", "external_name", "descriptor", "fingerprint", "catalog_revision", "first_seen_at", "last_seen_at", "retired_at")
	requireJSONKeys(t, CatalogPage{}, "catalog", "items", "next_cursor")
	requireJSONKeys(t, ServerMutation{}, "server", "operation")
	requireJSONKeys(t, CredentialReplacementResult{}, "server_id", "kind", "credential_revision", "operation")
	requireJSONKeys(t, AuthFlowCreation{}, "flow", "authorization_url")
}
