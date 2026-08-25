package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePrincipalService struct {
	items        []contract.Principal
	create       authorization.CreatePrincipalRequest
	patch        authorization.PatchPrincipalRequest
	cursor       *authorization.SnapshotCursor
	limit        int
	err          error
	defaultGrant contract.Grant
}

func (service *fakePrincipalService) CreatePrincipal(_ context.Context, request authorization.CreatePrincipalRequest) (contract.PrincipalCreation, error) {
	if service.err != nil {
		return contract.PrincipalCreation{}, service.err
	}
	service.create = request
	principal := contract.Principal{ID: testID, DisplayName: request.DisplayName, State: contract.PrincipalActive, Visibility: request.Visibility, Revision: "1", CredentialRevision: "0", CreatedAt: "2026-08-25T00:00:00Z", UpdatedAt: "2026-08-25T00:00:00Z"}
	service.items = append(service.items, principal)
	return contract.PrincipalCreation{Principal: principal, DefaultGrant: service.defaultGrant}, nil
}
func (service *fakePrincipalService) GetPrincipal(_ context.Context, id string) (contract.Principal, error) {
	if service.err != nil {
		return contract.Principal{}, service.err
	}
	for _, principal := range service.items {
		if principal.ID == id {
			return principal, nil
		}
	}
	return contract.Principal{}, authorization.ErrNotFound
}
func (service *fakePrincipalService) ListPrincipals(_ context.Context, cursor *authorization.SnapshotCursor, limit int) (authorization.PrincipalPage, error) {
	if service.err != nil {
		return authorization.PrincipalPage{}, service.err
	}
	service.cursor, service.limit = cursor, limit
	page := authorization.PrincipalPage{Items: append([]contract.Principal(nil), service.items...)}
	if len(service.items) != 0 && limit == 1 {
		page.Next = &authorization.SnapshotCursor{Collection: "principals", Upper: 2, After: 1, AfterID: service.items[0].ID}
		page.Items = page.Items[:1]
	}
	return page, nil
}
func (service *fakePrincipalService) PatchPrincipal(_ context.Context, id string, request authorization.PatchPrincipalRequest) (contract.Principal, error) {
	if service.err != nil {
		return contract.Principal{}, service.err
	}
	if len(service.items) == 0 || service.items[0].ID != id {
		return contract.Principal{}, authorization.ErrNotFound
	}
	if request.ExpectedRevision != service.items[0].Revision {
		return contract.Principal{}, authorization.ErrStaleRevision
	}
	service.patch = request
	if request.DisplayName != nil {
		service.items[0].DisplayName = *request.DisplayName
	}
	if request.State != nil {
		service.items[0].State = *request.State
	}
	if request.Visibility != nil {
		service.items[0].Visibility = *request.Visibility
	}
	service.items[0].Revision = "2"
	return service.items[0], nil
}

func principalResource() contract.Principal {
	return contract.Principal{ID: testID, DisplayName: "Agent", State: contract.PrincipalActive, Visibility: contract.VisibilityRequestable, Revision: "1", CredentialRevision: "0", CreatedAt: "2026-08-25T00:00:00Z", UpdatedAt: "2026-08-25T00:00:00Z"}
}

func newPrincipalHandler(t *testing.T, service PrincipalService, invalidations *[]contract.Invalidation) http.Handler {
	t.Helper()
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Principals: service, Invalidate: func(event contract.Invalidation) {
		if invalidations != nil {
			*invalidations = append(*invalidations, event)
		}
	}})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

func TestPrincipalCreateGetPatchListAndInvalidation(t *testing.T) {
	service := &fakePrincipalService{defaultGrant: contract.Grant{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", PrincipalID: testID, Effect: contract.GrantAllow, ServerID: contract.SyntheticServerID, State: contract.GrantActive, CreatedAt: "2026-08-25T00:00:00Z"}}
	var invalidations []contract.Invalidation
	handler := newPrincipalHandler(t, service, &invalidations)
	authJSON := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(handler, http.MethodPost, "/api/v1/principals", `{"display_name":"Agent","visibility":"requestable"}`, authJSON)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "1"), created.Header().Get("ETag"))
	assert.Empty(t, created.Header().Get("Location"))
	assert.Contains(t, created.Body.String(), `"default_grant"`)

	got := perform(handler, http.MethodGet, "/api/v1/principals/"+testID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "1"), got.Header().Get("ETag"))

	patch := perform(handler, http.MethodPatch, "/api/v1/principals/"+testID, `{"display_name":"Renamed","state":"disabled","visibility":"all"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")})
	require.Equal(t, http.StatusOK, patch.Code, patch.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "2"), patch.Header().Get("ETag"))
	assert.Equal(t, "1", service.patch.ExpectedRevision)

	listed := perform(handler, http.MethodGet, "/api/v1/principals?limit=1", "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Equal(t, 1, service.limit)
	assert.Contains(t, listed.Body.String(), `"next_cursor":"`)
	assert.Equal(t, "no-store", listed.Header().Get("Cache-Control"))
	assert.Empty(t, listed.Header().Get("Access-Control-Allow-Origin"))
	var page contract.Collection[contract.Principal]
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
	require.NotNil(t, page.NextCursor)
	continued := perform(handler, http.MethodGet, "/api/v1/principals?limit=2&cursor="+*page.NextCursor, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
	require.NotNil(t, service.cursor)
	assert.Equal(t, authorization.SnapshotCursor{Collection: "principals", Upper: 2, After: 1, AfterID: testID}, *service.cursor)
	assert.Equal(t, []contract.Invalidation{{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus}, {Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus}}, invalidations)
}

func TestPrincipalStrictValidationQueryAndPreconditions(t *testing.T) {
	service := &fakePrincipalService{items: []contract.Principal{principalResource()}}
	handler := newPrincipalHandler(t, service, nil)
	authJSON := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, test := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
		code                     string
	}{
		{"create null", http.MethodPost, "/api/v1/principals", `{"display_name":null,"visibility":"all"}`, authJSON, 400, "invalid_principal"},
		{"create missing", http.MethodPost, "/api/v1/principals", `{"display_name":"Agent"}`, authJSON, 400, "invalid_principal"},
		{"create unknown", http.MethodPost, "/api/v1/principals", `{"display_name":"Agent","visibility":"all","extra":true}`, authJSON, 400, "invalid_json"},
		{"create duplicate", http.MethodPost, "/api/v1/principals", `{"display_name":"Agent","display_name":"Other","visibility":"all"}`, authJSON, 400, "invalid_json"},
		{"create query", http.MethodPost, "/api/v1/principals?limit=1", `{"display_name":"Agent","visibility":"all"}`, authJSON, 400, "malformed_request"},
		{"patch empty", http.MethodPatch, "/api/v1/principals/" + testID, `{}`, authJSON, 400, "invalid_principal"},
		{"patch null", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":null}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")}, 400, "invalid_principal"},
		{"missing precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, authJSON, 428, "principal_precondition_required"},
		{"weak precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": "W/" + contract.PrincipalETag(testID, "1")}, 412, "stale_principal_revision"},
		{"wildcard precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": "*"}, 412, "stale_principal_revision"},
		{"wrong principal precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag("01ARZ3NDEKTSV4RRFFQ69G5FAX", "1")}, 412, "stale_principal_revision"},
		{"noncanonical precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "01")}, 412, "stale_principal_revision"},
		{"list precondition", http.MethodPatch, "/api/v1/principals/" + testID, `{"state":"disabled"}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1") + ", " + contract.PrincipalETag(testID, "2")}, 412, "stale_principal_revision"},
		{"unknown query", http.MethodGet, "/api/v1/principals?state=active", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "malformed_request"},
		{"repeated query", http.MethodGet, "/api/v1/principals?limit=1&limit=2", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "malformed_request"},
		{"empty query", http.MethodGet, "/api/v1/principals?limit=", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "malformed_request"},
		{"null query", http.MethodGet, "/api/v1/principals?cursor=null", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "malformed_request"},
		{"malformed escape", http.MethodGet, "/api/v1/principals?cursor=%ZZ", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "malformed_request"},
		{"bad cursor", http.MethodGet, "/api/v1/principals?cursor=abc", "", map[string]string{"Authorization": "Bearer " + testBearer}, 400, "invalid_cursor"},
		{"credential route deferred", http.MethodPost, "/api/v1/principals/" + testID + "/credential", `{}`, authJSON, 404, "not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := perform(handler, test.method, test.path, test.body, test.headers)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.code)
		})
	}
}

func TestPrincipalAuthenticationSessionAndErrorMapping(t *testing.T) {
	service := &fakePrincipalService{items: []contract.Principal{principalResource()}}
	handler := newPrincipalHandler(t, service, nil)
	unauthenticated := perform(handler, http.MethodGet, "/api/v1/principals", "", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	session := perform(handler, http.MethodPatch, "/api/v1/principals/"+testID, `{"visibility":"all"}`, map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")})
	assert.Equal(t, http.StatusOK, session.Code, session.Body.String())
	missingOrigin := perform(handler, http.MethodPatch, "/api/v1/principals/"+testID, `{"visibility":"all"}`, map[string]string{"Cookie": contract.SessionCookieName + "=session", "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "2")})
	assert.Equal(t, http.StatusForbidden, missingOrigin.Code)

	for _, test := range []struct {
		err  error
		code string
	}{
		{authorization.ErrNotFound, "not_found"}, {authorization.ErrInvalidInput, "invalid_principal"}, {authorization.ErrResourceLimit, "resource_limit"}, {authorization.ErrStaleRevision, "stale_principal_revision"}, {authorization.ErrConflict, "conflict"}, {authorization.ErrStaleCursor, "stale_cursor"}, {authorization.ErrShuttingDown, "shutting_down"}, {authorization.ErrStorageUnavailable, "authorization_unavailable"}, {errors.New("foreign"), "authorization_unavailable"},
	} {
		service.err = test.err
		response := perform(handler, http.MethodGet, "/api/v1/principals", "", map[string]string{"Authorization": "Bearer " + testBearer})
		assert.Contains(t, response.Body.String(), test.code, test.err)
	}
}
