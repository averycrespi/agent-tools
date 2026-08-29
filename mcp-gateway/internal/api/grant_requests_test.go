package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
)

func TestGrantRequestListItemApproveRejectAndPrivacy(t *testing.T) {
	service := &fakeGrantRequestService{item: adminRequestFixture()}
	state := contract.RequestPending
	service.page = grantrequests.AdminPage{
		Items: []contract.GrantRequestSummary{service.item.GrantRequestSummary},
		Next:  &grantrequests.AdminCursor{Collection: "grant_requests", PrincipalID: testID, State: &state, Upper: 2, After: 1, AfterID: service.item.ID},
	}
	handler := newGrantRequestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	listed := perform(handler, http.MethodGet, "/api/v1/grant-requests?limit=1&principal_id="+testID+"&state=pending", "", headers)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.NotContains(t, listed.Body.String(), "submitted_evidence")
	assert.NotContains(t, listed.Body.String(), "resolved_server_id")
	assert.Equal(t, grantrequests.AdminFilter{PrincipalID: testID, State: &state}, service.filter)
	var page contract.Collection[contract.GrantRequestSummary]
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
	require.NotNil(t, page.NextCursor)
	continued := perform(handler, http.MethodGet, "/api/v1/grant-requests?limit=2&principal_id="+testID+"&state=pending&cursor="+*page.NextCursor, "", headers)
	require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
	require.NotNil(t, service.cursor)
	assert.Equal(t, int64(2), service.cursor.Upper)

	item := perform(handler, http.MethodGet, "/api/v1/grant-requests/"+service.item.ID, "", headers)
	require.Equal(t, http.StatusOK, item.Code, item.Body.String())
	assert.Equal(t, contract.GrantRequestETag(service.item.ID, "1"), item.Header().Get("ETag"))
	assert.Contains(t, item.Body.String(), `"submitted_evidence"`)
	assert.Contains(t, item.Body.String(), `"current_target"`)
	assert.Empty(t, item.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "no-store", item.Header().Get("Cache-Control"))

	approveHeaders := map[string]string{
		"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON,
		"If-Match": contract.GrantRequestETag(service.item.ID, "1"),
	}
	approved := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/approve", `{"approved_policy":{"scope":"server","target":"sample","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true}}`, approveHeaders)
	require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	assert.Equal(t, "1", service.revision)
	assert.Equal(t, contract.PolicyServer, service.policy.Scope)
	assert.Equal(t, contract.GrantRequestETag(service.item.ID, "2"), approved.Header().Get("ETag"))

	service.item.Revision = "2"
	rejectHeaders := map[string]string{
		"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON,
		"If-Match": contract.GrantRequestETag(service.item.ID, "2"),
	}
	rejected := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/reject", `{"reason":"policy_conflict"}`, rejectHeaders)
	require.Equal(t, http.StatusOK, rejected.Code, rejected.Body.String())
	assert.Equal(t, contract.RejectionPolicyConflict, service.reason)
}

func TestGrantRequestStrictQueriesBodiesPreconditionsAndProblems(t *testing.T) {
	service := &fakeGrantRequestService{item: adminRequestFixture()}
	handler := newGrantRequestHandler(t, service)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	for _, path := range []string{
		"/api/v1/grant-requests?unknown=x", "/api/v1/grant-requests?limit=1&limit=2",
		"/api/v1/grant-requests?principal_id=", "/api/v1/grant-requests?state=unknown", "/api/v1/grant-requests?cursor=%ZZ",
	} {
		response := perform(handler, http.MethodGet, path, "", headers)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "malformed_request")
	}
	badCursor := perform(handler, http.MethodGet, "/api/v1/grant-requests?cursor=abc", "", headers)
	assert.Equal(t, http.StatusBadRequest, badCursor.Code)
	assert.Contains(t, badCursor.Body.String(), "invalid_cursor")

	body := `{"approved_policy":{"scope":"server","target":"sample","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true}}`
	contentHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	missing := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/approve", body, contentHeaders)
	assert.Equal(t, 428, missing.Code)
	assert.Contains(t, missing.Body.String(), "grant_request_precondition_required")
	for _, etag := range []string{"*", `W/` + contract.GrantRequestETag(service.item.ID, "1"), contract.GrantRequestETag(testServerID, "1"), `"grant-request-` + service.item.ID + `-01"`} {
		invalidHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": etag}
		response := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/approve", body, invalidHeaders)
		assert.Equal(t, http.StatusPreconditionFailed, response.Code, etag)
	}
	validHeaders := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.GrantRequestETag(service.item.ID, "1")}
	for _, invalidBody := range []string{
		`{}`, `{"approved_policy":null}`, `{"approved_policy":{"scope":"server","target":"sample","constraint":null,"duration_seconds":null}}`,
		`{"approved_policy":{"scope":"server","target":"sample","constraint":null,"duration_seconds":null,"future_tools_acknowledged":true,"extra":1}}`,
	} {
		response := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/approve", invalidBody, validHeaders)
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{grantrequests.ErrNotFound, 404, "not_found"},
		{grantrequests.ErrInvalidInput, 400, "invalid_grant_request"},
		{grantrequests.ErrConflict, 409, "grant_request_conflict"},
		{grantrequests.ErrStaleRevision, 412, "stale_grant_request_revision"},
		{grantrequests.ErrResourceLimit, 429, "resource_limit"},
		{grantrequests.ErrStaleCursor, 409, "stale_cursor"},
		{grantrequests.ErrStorageUnavailable, 503, "authorization_unavailable"},
		{errors.New("foreign"), 503, "authorization_unavailable"},
	} {
		service.err = test.err
		response := perform(handler, http.MethodGet, "/api/v1/grant-requests/"+service.item.ID, "", headers)
		assert.Equal(t, test.status, response.Code, test.err)
		assert.Contains(t, response.Body.String(), test.code, test.err)
	}
	missingRoute := perform(handler, http.MethodGet, "/api/v1/grant-requests/"+service.item.ID+"/approve/extra", "", headers)
	assert.Equal(t, http.StatusNotFound, missingRoute.Code)
}

func TestGrantRequestSessionOriginAndCSRFAreEnforced(t *testing.T) {
	service := &fakeGrantRequestService{item: adminRequestFixture()}
	handler := newGrantRequestHandler(t, service)
	sessionHeaders := map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin}
	listed := perform(handler, http.MethodGet, "/api/v1/grant-requests", "", sessionHeaders)
	assert.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	body := `{"reason":"not_approved"}`
	missingCSRF := perform(handler, http.MethodPost, "/api/v1/grant-requests/"+service.item.ID+"/reject", body, map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin,
		"Content-Type": contract.MediaTypeJSON, "If-Match": contract.GrantRequestETag(service.item.ID, "1"),
	})
	assert.Equal(t, http.StatusUnauthorized, missingCSRF.Code)
	missingOrigin := perform(handler, http.MethodGet, "/api/v1/grant-requests", "", map[string]string{"Cookie": contract.SessionCookieName + "=session"})
	assert.Equal(t, http.StatusForbidden, missingOrigin.Code)
}

func newGrantRequestHandler(t *testing.T, service GrantRequestService) http.Handler {
	t.Helper()
	handler := New(Options{
		Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{},
		GrantRequests: service, Invalidate: func(contract.Invalidation) {},
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

type fakeGrantRequestService struct {
	page     grantrequests.AdminPage
	item     contract.GrantRequest
	filter   grantrequests.AdminFilter
	cursor   *grantrequests.AdminCursor
	revision string
	policy   contract.Policy
	reason   contract.GrantRequestRejectionReason
	err      error
}

func (service *fakeGrantRequestService) ListAdmin(_ context.Context, filter grantrequests.AdminFilter, cursor *grantrequests.AdminCursor, _ int) (grantrequests.AdminPage, error) {
	service.filter, service.cursor = filter, cursor
	return service.page, service.err
}

func (service *fakeGrantRequestService) GetAdmin(context.Context, string) (contract.GrantRequest, error) {
	return service.item, service.err
}

func (service *fakeGrantRequestService) ApproveAdmin(_ context.Context, _ string, revision string, policy contract.Policy) (contract.GrantRequest, error) {
	service.revision, service.policy = revision, policy
	item := service.item
	item.Revision = "2"
	return item, service.err
}

func (service *fakeGrantRequestService) RejectAdmin(_ context.Context, _ string, revision string, reason contract.GrantRequestRejectionReason) (contract.GrantRequest, error) {
	service.revision, service.reason = revision, reason
	return service.item, service.err
}

func adminRequestFixture() contract.GrantRequest {
	evidence := contract.DescriptorEvidence{
		ServerID: testServerID, ToolID: testID, Namespace: "sample", UpstreamName: "echo", ExternalName: "sample.echo",
		CatalogRevision: "1", Fingerprint: "sha256:fixture", DurableState: contract.EvidenceCurrent,
		Descriptor: contract.NormalizedToolDescriptor{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
		CapturedAt: "2026-08-27T00:00:00.000000000Z",
	}
	active, durable := contract.TargetActiveCurrent, contract.TargetDurableCurrent
	return contract.GrantRequest{
		GrantRequestSummary: contract.GrantRequestSummary{
			ID: testID, PrincipalID: testID, State: contract.RequestPending, Revision: "1",
			RequestedPolicy: contract.Policy{Scope: contract.PolicyTool, Target: "sample.echo"},
			CreatedAt:       "2026-08-27T00:00:00.000000000Z", UpdatedAt: "2026-08-27T00:00:00.000000000Z",
		},
		ResolvedServerID: testServerID, ResolvedUpstreamName: stringPointerAPI("echo"), SubmittedEvidence: &evidence,
		CurrentTarget: contract.TargetComparison{Scope: contract.PolicyTool, TargetState: contract.TargetExtant, ActiveState: &active, DurableState: &durable},
	}
}

func stringPointerAPI(value string) *string { return &value }
