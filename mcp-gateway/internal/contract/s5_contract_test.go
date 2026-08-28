package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/gowebpki/jcs"
	"github.com/stretchr/testify/require"
)

func TestS5SyntheticDescriptorsAreExactAndPinned(t *testing.T) {
	t.Parallel()
	expected := []struct {
		id, upstream, external, title, description, input, fingerprint string
		readOnly, destructive, idempotent                              bool
	}{
		{"00000000000000000000000001", "get_identity", "mcp_gateway.get_identity", "Get identity", "Return the authenticated principal's identity.", `{"type":"object","additionalProperties":false}`, "cc982af50fbc4873c57e89b5052a3c725f5e3898b2142dab096b99b0a4e656b9", true, false, true},
		{"00000000000000000000000002", "list_grants", "mcp_gateway.list_grants", "List grants", "List grants belonging to the authenticated principal.", `{"type":"object","additionalProperties":false,"required":["cursor"],"properties":{"cursor":{"type":["string","null"],"maxLength":512}}}`, "6c400b6b857af7b7f6b58dc8254c8e9d3ea1d064ba1168e01c049aae3f0ea1f4", true, false, true},
		{"00000000000000000000000003", "create_grant_request", "mcp_gateway.create_grant_request", "Create grant request", "Create or return an identical pending access request for the authenticated principal.", `{"type":"object","additionalProperties":false,"required":["policy"],"properties":{"policy":{"type":"object","additionalProperties":false,"required":["scope","target","constraint","duration_seconds","future_tools_acknowledged"],"properties":{"scope":{"enum":["tool","server"]},"target":{"type":"string","minLength":1,"maxLength":128},"constraint":{"anyOf":[{"type":"null"},{"type":"object","additionalProperties":false,"required":["equals"],"properties":{"equals":{"type":"object","minProperties":1,"maxProperties":16,"propertyNames":{"type":"string","minLength":1,"maxLength":256},"additionalProperties":{"type":["string","boolean","number","null"]}}}}]},"duration_seconds":{"anyOf":[{"type":"null"},{"type":"string","pattern":"^(?:[6-9][0-9]|[1-9][0-9]{2,5}|[12][0-9]{6})$"}]},"future_tools_acknowledged":{"type":"boolean"}}}}}`, "eb14cf705b755301f7f6aab0bcb5bb396d296ce21686521425011d2afe0bb6e4", false, false, false},
		{"00000000000000000000000004", "get_grant_request", "mcp_gateway.get_grant_request", "Get grant request", "Return one grant request belonging to the authenticated principal.", `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":"^[0-9A-HJKMNP-TV-Z]{26}$"}}}`, "5906ef2ea8364faabc43416cc6953ae3c5438f74615f028fbb87290d58387b5d", true, false, true},
		{"00000000000000000000000005", "list_grant_requests", "mcp_gateway.list_grant_requests", "List grant requests", "List grant requests belonging to the authenticated principal.", `{"type":"object","additionalProperties":false,"required":["cursor","state"],"properties":{"cursor":{"type":["string","null"],"maxLength":512},"state":{"enum":[null,"pending","approved","rejected","cancelled"]}}}`, "3189c7d50d7bc6709d13a1c25ec21a1c1128acf362ec0567b9f315b4cc1666fa", true, false, true},
		{"00000000000000000000000006", "cancel_grant_request", "mcp_gateway.cancel_grant_request", "Cancel grant request", "Cancel one pending grant request belonging to the authenticated principal.", `{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":"^[0-9A-HJKMNP-TV-Z]{26}$"}}}`, "f6414fe9dff78271e7a6851a70252b2b94e49c7b4399d145a55efbc51654e73f", false, true, true},
	}
	tools := SyntheticSelfServiceTools()
	require.Len(t, tools, 6)
	for index, want := range expected {
		tool := tools[index]
		require.Equal(t, want.id, tool.ID)
		require.Equal(t, SyntheticServerID, tool.ServerID)
		require.Equal(t, want.upstream, tool.UpstreamName)
		require.Equal(t, want.external, tool.ExternalName)
		require.Equal(t, "1", tool.CatalogRevision)
		require.Equal(t, want.title, *tool.Descriptor.Title)
		require.Equal(t, want.description, *tool.Descriptor.Description)
		require.JSONEq(t, want.input, string(tool.Descriptor.InputSchema))
		require.Empty(t, tool.Descriptor.OutputSchema)
		require.Equal(t, NormalizedToolAnnotations{Title: &want.title, ReadOnlyHint: want.readOnly, DestructiveHint: want.destructive, IdempotentHint: want.idempotent, OpenWorldHint: false}, tool.Descriptor.Annotations)
		encoded, err := json.Marshal(tool.Descriptor)
		require.NoError(t, err)
		canonical, err := jcs.Transform(encoded)
		require.NoError(t, err)
		require.Equal(t, canonical, []byte(tool.Canonical))
		digest := sha256.Sum256(canonical)
		require.Equal(t, hex.EncodeToString(digest[:]), tool.Fingerprint)
		require.Equal(t, want.fingerprint, tool.Fingerprint)
	}
	tools[0].Descriptor.InputSchema[0] = 'x'
	tools[0].Canonical[0] = 'x'
	*tools[0].Descriptor.Title = "changed"
	fresh := SyntheticSelfServiceTools()[0]
	require.JSONEq(t, expected[0].input, string(fresh.Descriptor.InputSchema))
	require.Equal(t, expected[0].fingerprint, fresh.Fingerprint)
	require.Equal(t, expected[0].title, *fresh.Descriptor.Title)
}

func TestS5ClosedVocabularyRoutesProblemsAndLimitsAreExact(t *testing.T) {
	t.Parallel()
	require.Equal(t, []GrantRequestState{RequestPending, RequestApproved, RequestRejected, RequestCancelled}, GrantRequestStates())
	require.Equal(t, []PolicyScope{PolicyTool, PolicyServer}, PolicyScopes())
	require.Equal(t, []GrantRequestRejectionReason{RejectionNotApproved, RejectionExistingAccess, RejectionScopeTooBroad, RejectionPolicyConflict}, GrantRequestRejectionReasons())
	require.Equal(t, []TargetState{TargetExtant, TargetDeleted}, TargetStates())
	require.Equal(t, []TargetActiveState{TargetActiveCurrent, TargetActiveStale, TargetActiveAbsent, TargetActiveUnavailable}, TargetActiveStates())
	require.Equal(t, []TargetDurableState{TargetDurableCurrent, TargetDurableRetired, TargetDurableAbsent}, TargetDurableStates())
	require.Equal(t, []DescriptorEvidenceState{EvidenceCurrent, EvidenceRetired}, DescriptorEvidenceStates())
	require.Equal(t, []CursorOutcome{CursorOK, CursorInvalid, CursorStale}, CursorOutcomes())
	require.Equal(t, []CreateGrantRequestOutcome{RequestCreated, RequestExisting, RequestDenyConflict, RequestTargetUnavailable, RequestLimitReached}, CreateGrantRequestOutcomes())
	require.Equal(t, []GetGrantRequestOutcome{RequestFound, RequestNotFound}, GetGrantRequestOutcomes())
	require.Equal(t, []CancelGrantRequestOutcome{RequestCancellationCancelled, RequestCancellationAlreadyCancelled, RequestCancellationNotPending, RequestCancellationNotFound}, CancelGrantRequestOutcomes())
	require.Equal(t, []InvalidationKind{InvalidationGrantRequests}, InvalidationKinds()[len(InvalidationKinds())-1:])
	for _, parse := range []func(string) error{
		func(value string) error { _, err := ParseGrantRequestState(value); return err },
		func(value string) error { _, err := ParsePolicyScope(value); return err },
		func(value string) error { _, err := ParseGrantRequestRejectionReason(value); return err },
	} {
		require.Error(t, parse("unknown"))
	}

	expectedRoutes := []Route{
		{Pattern: "/api/v1/grant-requests", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/grant-requests/{id}", Methods: []string{"GET"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/grant-requests/{id}/approve", Methods: []string{"POST"}, Authority: AuthorityAdmin},
		{Pattern: "/api/v1/grant-requests/{id}/reject", Methods: []string{"POST"}, Authority: AuthorityAdmin},
	}
	routes := Routes()
	routeStart := -1
	for index, route := range routes {
		if route.Pattern == expectedRoutes[0].Pattern {
			routeStart = index
			break
		}
	}
	require.NotEqual(t, -1, routeStart)
	require.Equal(t, expectedRoutes, routes[routeStart:routeStart+len(expectedRoutes)])
	expectedProblems := []Problem{
		{Status: 400, Code: ProblemInvalidGrantRequest, Title: "The grant request is invalid."},
		{Status: 409, Code: ProblemGrantRequestConflict, Title: "The grant request conflicts with current state."},
		{Status: 412, Code: ProblemStaleGrantRequestRevision, Title: "The grant request revision is stale."},
		{Status: 428, Code: ProblemGrantRequestPreconditionRequired, Title: "The current grant request revision is required."},
	}
	problems := Problems()
	require.Equal(t, expectedProblems, problems[len(problems)-4:])
	for name, maximum := range map[string]int64{
		"discoverable_tools": 2054, "grant_requests": 4096, "pending_grant_requests_per_principal": 128,
		"grant_request_evidence_bytes": 268435456, "grant_request_evidence_snapshot_bytes": 135168,
		"agent_self_service_list_page": 100, "grant_request_target_bytes": 128,
	} {
		limit, ok := FixedLimitByName(name)
		require.True(t, ok, name)
		require.Equal(t, maximum, limit.Maximum, name)
		require.True(t, limit.Allows(maximum), name)
		require.False(t, limit.Allows(maximum+1), name)
	}
	require.Equal(t, int64(60), GrantRequestDurationMinimumSeconds)
	require.Equal(t, int64(2592000), GrantRequestDurationMaximumSeconds)
}

func TestS5ResourceShapesETagsMechanicsAndStatusAreExact(t *testing.T) {
	t.Parallel()
	policy := Policy{Scope: PolicyTool, Target: "server.tool"}
	request := AgentGrantRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", State: RequestPending, Revision: "1", RequestedPolicy: policy}
	requireJSONKeys(t, policy, "scope", "target", "constraint", "duration_seconds", "future_tools_acknowledged")
	requireJSONKeys(t, GrantPolicy{}, "scope", "target", "constraint")
	requireJSONKeys(t, SelfIdentity{}, "id", "display_name", "state", "visibility", "principal_revision", "credential_revision")
	requireJSONKeys(t, AgentGrant{}, "id", "effect", "policy", "expires_at", "state", "created_at")
	requireJSONKeys(t, request, "id", "state", "revision", "requested_policy", "approved_policy", "approved_grant_id", "rejection_reason", "created_at", "updated_at", "closed_at")
	requireJSONKeys(t, DescriptorEvidence{}, "server_id", "tool_id", "namespace", "upstream_name", "external_name", "catalog_revision", "fingerprint", "durable_state", "descriptor", "captured_at")
	requireJSONKeys(t, TargetComparison{}, "scope", "target_state", "active_state", "durable_state", "catalog_revision", "fingerprint", "descriptor")
	requireJSONKeys(t, GrantRequestSummary{}, "id", "principal_id", "state", "revision", "requested_policy", "approved_policy", "approved_grant_id", "rejection_reason", "created_at", "updated_at", "closed_at")
	requireJSONKeys(t, GrantRequest{}, "id", "principal_id", "state", "revision", "requested_policy", "approved_policy", "approved_grant_id", "rejection_reason", "created_at", "updated_at", "closed_at", "resolved_server_id", "resolved_upstream_name", "submitted_evidence", "approved_evidence", "current_target")
	requireJSONKeys(t, GrantRequestApproval{}, "approved_policy")
	requireJSONKeys(t, GrantRequestRejection{}, "reason")
	requireJSONKeys(t, GetIdentityResult{}, "identity")
	requireJSONKeys(t, ListGrantsInput{}, "cursor")
	requireJSONKeys(t, ListGrantsResult{}, "outcome", "items", "next_cursor")
	requireJSONKeys(t, CreateGrantRequestInput{}, "policy")
	requireJSONKeys(t, CreateGrantRequestResult{}, "outcome", "request")
	requireJSONKeys(t, GrantRequestIDInput{}, "id")
	requireJSONKeys(t, GetGrantRequestResult{}, "outcome", "request")
	requireJSONKeys(t, ListGrantRequestsInput{}, "cursor", "state")
	requireJSONKeys(t, ListGrantRequestsResult{}, "outcome", "items", "next_cursor")
	requireJSONKeys(t, CancelGrantRequestResult{}, "outcome", "request")
	require.Equal(t, []string{SummaryIdentityReturned, SummaryGrantsReturned, SummaryGrantRequestProcessed, SummaryGrantRequestReturned, SummaryGrantRequestsReturned, SummaryGrantRequestCancellationProcessed}, []string{"Identity returned.", "Grants returned.", "Grant request processed.", "Grant request returned.", "Grant requests returned.", "Grant request cancellation processed."})
	requireJSONKeys(t, LimitsStatus{}, "http_regular", "http_control_auth", "http_admin", "http_health", "mcp_work", "mcp_streams", "admin_sessions", "legacy_sessions", "event_streams", "backup_work", "backup_records", "admin_credentials", "idempotency_records", "keyring_candidates", "keyring_work", "database_bytes", "server_identities", "servers", "downstream_runtimes", "server_reconciliations", "catalog_traversals", "oauth_flows", "oauth_callback_work", "s2_idempotency_records", "active_tools", "durable_tool_identities", "downstream_dispatch", "principals", "grants", "grant_requests", "grant_request_evidence_bytes")

	etag := GrantRequestETag(request.ID, request.Revision)
	require.Equal(t, `"grant-request-01ARZ3NDEKTSV4RRFFQ69G5FAV-1"`, etag)
	require.True(t, MatchesGrantRequestETag(etag, request.ID, request.Revision))
	for _, invalid := range []string{"", "*", `W/` + etag, etag + ", " + etag, `"grant-request-01ARZ3NDEKTSV4RRFFQ69G5FAV-2"`} {
		require.False(t, MatchesGrantRequestETag(invalid, request.ID, request.Revision), invalid)
	}

	mechanics := ResourceMechanics()
	expectedMechanics := []ResourceMechanic{
		{Pattern: "/api/v1/grant-requests", Method: "GET", RequestSchema: "GrantRequestListQuery", SuccessSchema: "Page<GrantRequestSummary>", SuccessStatuses: []int{200}, Cursor: true},
		{Pattern: "/api/v1/grant-requests/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, ETag: true},
		{Pattern: "/api/v1/grant-requests/{id}/approve", Method: "POST", RequestSchema: "GrantRequestApproval", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
		{Pattern: "/api/v1/grant-requests/{id}/reject", Method: "POST", RequestSchema: "GrantRequestRejection", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	}
	start := -1
	for index, mechanic := range mechanics {
		if mechanic.Pattern == expectedMechanics[0].Pattern && mechanic.Method == expectedMechanics[0].Method {
			start = index
			break
		}
	}
	require.GreaterOrEqual(t, start, 0)
	require.Equal(t, expectedMechanics, mechanics[start:start+len(expectedMechanics)])
}

func TestS5AcceptanceAndClauseManifestsAreCompleteAndCopySafe(t *testing.T) {
	t.Parallel()
	criteria := S5AcceptanceEvidenceManifest()
	require.Equal(t, []string{"AC-1", "AC-2", "AC-3", "AC-4", "AC-5", "AC-6", "AC-7"}, criterionNames(criteria))
	seenEvidence := map[string]bool{}
	for _, entry := range criteria {
		require.NotEmpty(t, entry.Evidence)
		for _, evidence := range entry.Evidence {
			require.Regexp(t, `^s5-[a-z0-9-]+$`, evidence)
			seenEvidence[evidence] = true
		}
	}
	clauses := S5ClauseEvidenceManifest()
	require.Equal(t, requiredS5ClauseIDs(), clauseNames(clauses))
	seenTasks := map[string]bool{}
	for _, clause := range clauses {
		require.NotEmpty(t, clause.Tasks, clause.Clause)
		require.NotEmpty(t, clause.Evidence, clause.Clause)
		for _, task := range clause.Tasks {
			require.Regexp(t, `^T(?:[1-9]|1[0-9]|2[0-7])$`, task)
			seenTasks[task] = true
		}
		for _, evidence := range clause.Evidence {
			require.True(t, seenEvidence[evidence], "%s: %s", clause.Clause, evidence)
		}
	}
	for task := 1; task <= 27; task++ {
		require.True(t, seenTasks["T"+itoa(task)], "T%d has no S5 clause assignment", task)
	}
	criteria[0].Evidence[0] = "changed"
	clauses[0].Tasks[0] = "changed"
	require.NotEqual(t, "changed", S5AcceptanceEvidenceManifest()[0].Evidence[0])
	require.NotEqual(t, "changed", S5ClauseEvidenceManifest()[0].Tasks[0])
}
