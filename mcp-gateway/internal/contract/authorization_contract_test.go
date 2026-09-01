package contract

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthorizationRoutesAndMechanicsAreExact(t *testing.T) {
	t.Parallel()

	expectedRoutes := []Route{
		{Pattern: "/api/v1/principals", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/principals/{id}", Methods: []string{"GET", "PATCH"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/principals/{id}/credential", Methods: []string{"DELETE", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/grants", Methods: []string{"GET", "POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/grants/{id}", Methods: []string{"DELETE", "GET", "PATCH"}, Authority: AuthorityAdmin},
	}
	routes := Routes()
	s3RouteStart := -1
	for index, route := range routes {
		if route.Pattern == expectedRoutes[0].Pattern {
			s3RouteStart = index
			break
		}
	}
	require.NotEqual(t, -1, s3RouteStart)
	require.Equal(t, expectedRoutes, routes[s3RouteStart:s3RouteStart+len(expectedRoutes)])
	routes[s3RouteStart].Methods[0] = "PATCH"
	require.Equal(t, expectedRoutes, Routes()[s3RouteStart:s3RouteStart+len(expectedRoutes)], "route copies must be isolated")

	for path, pattern := range map[string]string{
		"/api/v1/principals":                                       "/api/v1/principals",
		"/api/v1/principals/01ARZ3NDEKTSV4RRFFQ69G5FAV":            "/api/v1/principals/{id}",
		"/api/v1/principals/01ARZ3NDEKTSV4RRFFQ69G5FAV/credential": "/api/v1/principals/{id}/credential",
		"/api/v1/grants":                                           "/api/v1/grants",
		"/api/v1/grants/01ARZ3NDEKTSV4RRFFQ69G5FAV":                "/api/v1/grants/{id}",
	} {
		route, ok := RouteForPath(path)
		require.True(t, ok, path)
		require.Equal(t, pattern, route.Pattern, path)
	}
	for _, path := range []string{"/api/v1/principals/", "/api/v1/principals/a/credential/", "/api/v1/grants/a/b"} {
		_, ok := RouteForPath(path)
		require.False(t, ok, path)
	}

	expectedMechanics := []ResourceMechanic{
		{Pattern: "/api/v1/principals", Method: "GET", RequestSchema: "PrincipalListQuery", SuccessSchema: "Page<Principal>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/principals", Method: "POST", RequestSchema: "PrincipalCreate", SuccessSchema: "PrincipalCreation", SuccessStatuses: []int{201}, ETag: true},
		{Pattern: "/api/v1/principals/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Principal", SuccessStatuses: []int{200}, ETag: true},
		{Pattern: "/api/v1/principals/{id}", Method: "PATCH", RequestSchema: "PrincipalPatch", SuccessSchema: "Principal", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/principals/{id}/credential", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "AgentCredentialCreation", SuccessStatuses: []int{201}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/principals/{id}/credential", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "Principal", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/grants", Method: "GET", RequestSchema: "GrantListQuery", SuccessSchema: "Page<Grant>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/grants", Method: "POST", RequestSchema: "GrantCreate", SuccessSchema: "Grant", SuccessStatuses: []int{201}},
		{Pattern: "/api/v1/grants/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Grant", SuccessStatuses: []int{200}},
		{Pattern: "/api/v1/grants/{id}", Method: "DELETE", RequestSchema: "None", SuccessSchema: "Empty", SuccessStatuses: []int{204}},
	}
	mechanics := ResourceMechanics()
	s3MechanicStart := -1
	for index, mechanic := range mechanics {
		if mechanic.Pattern == expectedMechanics[0].Pattern && mechanic.Method == expectedMechanics[0].Method {
			s3MechanicStart = index
			break
		}
	}
	require.GreaterOrEqual(t, s3MechanicStart, 0)
	require.Equal(t, expectedMechanics, mechanics[s3MechanicStart:s3MechanicStart+len(expectedMechanics)])
	mechanics[s3MechanicStart].SuccessStatuses[0] = 500
	require.Equal(t, expectedMechanics, ResourceMechanics()[s3MechanicStart:s3MechanicStart+len(expectedMechanics)], "mechanic copies must be isolated")
	for _, mechanic := range expectedMechanics {
		require.False(t, mechanic.Idempotency, mechanic.Pattern)
	}
}

func TestAuthorizationProblemsLimitsAndProtocolVocabularyAreExact(t *testing.T) {
	t.Parallel()

	expectedProblems := []Problem{
		{Status: 400, Code: ProblemInvalidPrincipal, Title: "The principal is invalid."},
		{Status: 400, Code: ProblemInvalidGrant, Title: "The grant is invalid."},
		{Status: 412, Code: ProblemStaleGrantRevision, Title: "The grant revision is stale."},
		{Status: 428, Code: ProblemGrantPreconditionRequired, Title: "The current grant revision is required."},
		{Status: 412, Code: ProblemStalePrincipalRevision, Title: "The principal revision is stale."},
		{Status: 428, Code: ProblemPrincipalPreconditionRequired, Title: "The current principal revision is required."},
		{Status: 503, Code: ProblemAuthorizationUnavailable, Title: "Authorization is unavailable."},
	}
	problems := Problems()
	s3ProblemStart := slices.IndexFunc(problems, func(problem Problem) bool { return problem.Code == ProblemInvalidPrincipal })
	require.GreaterOrEqual(t, s3ProblemStart, 0)
	require.Equal(t, expectedProblems, problems[s3ProblemStart:s3ProblemStart+len(expectedProblems)])

	expectedLimits := []FixedLimit{
		{Name: "principals", Maximum: 128},
		{Name: "grants", Maximum: 4096},
		{Name: "constraint_atoms", Maximum: 16},
		{Name: "constraint_bytes", Maximum: 8192},
		{Name: "constraint_pointer_bytes", Maximum: 256},
		{Name: "grant_description_bytes", Maximum: 256},
	}
	for _, expected := range expectedLimits {
		limit, ok := FixedLimitByName(expected.Name)
		require.True(t, ok, expected.Name)
		require.Equal(t, expected, limit)
		require.True(t, limit.Allows(limit.Maximum), limit.Name)
		require.False(t, limit.Allows(limit.Maximum+1), limit.Name)
	}
	require.Equal(t, 50, S3ListPageDefault)
	require.Equal(t, []AgentAuthMode{AgentAuthDenyAll, AgentAuthPrincipalCredentials}, AgentAuthModes())
	require.Equal(t, AgentBearerPrefix, "mgw_agent_")
	require.Equal(t, []InvalidationKind{
		InvalidationAdminCredentials, InvalidationSystemStatus, InvalidationBackups, InvalidationServers,
		InvalidationServerOperations, InvalidationServerAuthFlows, InvalidationCatalog, InvalidationAuthorization,
	}, InvalidationKinds()[:8])
	require.Contains(t, InvalidationKinds(), InvalidationInvocations)
	require.Equal(t, SecretSinkAgentCredentialCreation, ApprovedSecretSinks()[7], "S3 sink must retain its historical prefix position")
}

func TestAuthorizationClosedStatesAndSyntheticIdentityAreExact(t *testing.T) {
	t.Parallel()

	require.Equal(t, "00000000000000000000000000", SyntheticServerID)
	require.Equal(t, "mcp_gateway", SyntheticServerNamespace)
	require.Equal(t, []PrincipalState{PrincipalActive, PrincipalDisabled}, PrincipalStates())
	require.Equal(t, []PrincipalVisibility{VisibilityRequestable, VisibilityAllowedOnly, VisibilityAll}, PrincipalVisibilities())
	require.Equal(t, []GrantEffect{GrantAllow, GrantDeny}, GrantEffects())
	require.Equal(t, []GrantState{GrantActive, GrantExpired}, GrantStates())
	require.Equal(t, []AuthorizationDecision{DecisionAllow, DecisionDeny, DecisionBlock}, AuthorizationDecisions())

	parsers := []struct {
		valid []string
		parse func(string) error
	}{
		{[]string{"active", "disabled"}, func(value string) error { _, err := ParsePrincipalState(value); return err }},
		{[]string{"requestable", "allowed-only", "all"}, func(value string) error { _, err := ParsePrincipalVisibility(value); return err }},
		{[]string{"allow", "deny"}, func(value string) error { _, err := ParseGrantEffect(value); return err }},
		{[]string{"active", "expired"}, func(value string) error { _, err := ParseGrantState(value); return err }},
		{[]string{"allow", "deny", "block"}, func(value string) error { _, err := ParseAuthorizationDecision(value); return err }},
	}
	for _, parser := range parsers {
		for _, value := range parser.valid {
			require.NoError(t, parser.parse(value), value)
		}
		require.Error(t, parser.parse("unknown"))
	}
}

func TestAuthorizationResourceShapesETagsAndStatusAreExact(t *testing.T) {
	t.Parallel()

	constraint := json.RawMessage(`{"equals":{"/count":1.0}}`)
	expires := "2026-08-26T00:00:00Z"
	credential := AgentCredential{ID: "credential", Fingerprint: "fingerprint", Revision: "1", CreatedAt: "2026-08-25T00:00:00Z"}
	principal := Principal{ID: "principal", DisplayName: "Agent", State: PrincipalActive, Visibility: VisibilityRequestable, Revision: "1", CredentialRevision: "1", Credential: &credential, CreatedAt: "2026-08-25T00:00:00Z", UpdatedAt: "2026-08-25T00:00:00Z"}
	description := "Example grant"
	grant := Grant{ID: "grant", Description: &description, Revision: "1", PrincipalID: principal.ID, Effect: GrantAllow, ServerID: SyntheticServerID, UpstreamName: nil, Constraint: &constraint, ExpiresAt: &expires, State: GrantActive, CreatedAt: "2026-08-25T00:00:00Z"}
	grantID := grant.ID

	requireJSONKeys(t, credential, "id", "fingerprint", "revision", "created_at")
	requireJSONKeys(t, principal, "id", "display_name", "state", "visibility", "revision", "credential_revision", "credential", "created_at", "updated_at")
	requireJSONKeys(t, PrincipalCreation{Principal: principal, DefaultGrant: grant}, "principal", "default_grant")
	requireJSONKeys(t, AgentCredentialCreation{Principal: principal, Bearer: "one-time"}, "principal", "bearer")
	requireJSONKeys(t, grant, "id", "description", "revision", "principal_id", "effect", "server_id", "upstream_name", "constraint", "expires_at", "state", "created_at")
	requireJSONKeys(t, AuthorizationResult{Decision: DecisionAllow, AuthorizationRevision: "1", EvaluatedAt: "2026-08-25T00:00:00Z", GrantID: &grantID}, "decision", "authorization_revision", "evaluated_at", "grant_id")

	grantETag := GrantETag("01ARZ3NDEKTSV4RRFFQ69G5FAV", "7")
	require.Equal(t, `"grant-01ARZ3NDEKTSV4RRFFQ69G5FAV-7"`, grantETag)
	require.True(t, MatchesGrantETag(grantETag, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "7"))

	etag := PrincipalETag("01ARZ3NDEKTSV4RRFFQ69G5FAV", "7")
	require.Equal(t, `"principal-01ARZ3NDEKTSV4RRFFQ69G5FAV-7"`, etag)
	require.True(t, MatchesPrincipalETag(etag, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "7"))
	for _, invalid := range []string{"", "*", `W/` + etag, etag + ", " + etag, `"principal-01ARZ3NDEKTSV4RRFFQ69G5FAV-8"`, `"principal-01ARZ3NDEKTSV4RRFFQ69G5FAA-7"`} {
		require.False(t, MatchesPrincipalETag(invalid, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "7"), invalid)
	}

	requireJSONKeys(t, LimitsStatus{},
		"http_regular", "http_control_auth", "http_admin", "http_health", "mcp_work", "mcp_streams", "admin_sessions", "legacy_sessions",
		"event_streams", "backup_work", "backup_records", "admin_credentials", "idempotency_records", "keyring_candidates", "keyring_work", "database_bytes",
		"server_identities", "servers", "downstream_runtimes", "server_reconciliations", "catalog_traversals", "oauth_flows", "oauth_callback_work",
		"s2_idempotency_records", "active_tools", "durable_tool_identities", "downstream_dispatch", "principals", "grants",
		"grant_requests", "grant_request_evidence_bytes",
	)
}
