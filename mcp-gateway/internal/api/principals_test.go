package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

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
	bearerSerial int
	grants       []contract.Grant
	grantCreate  authorization.CreateGrantRequest
	grantFilter  authorization.GrantFilter
	grantCursor  *authorization.SnapshotCursor
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
func (service *fakePrincipalService) IssueCredential(_ context.Context, id, revision string) (contract.AgentCredentialCreation, error) {
	if service.err != nil {
		return contract.AgentCredentialCreation{}, service.err
	}
	principal, err := service.credentialPrincipal(id, revision)
	if err != nil {
		return contract.AgentCredentialCreation{}, err
	}
	service.bearerSerial++
	principal.Revision = strconv.Itoa(service.bearerSerial + 1)
	principal.CredentialRevision = strconv.Itoa(service.bearerSerial)
	principal.Credential = &contract.AgentCredential{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Fingerprint: "0123456789abcdef", Revision: principal.CredentialRevision, CreatedAt: principal.UpdatedAt}
	service.items[0] = principal
	return contract.AgentCredentialCreation{Principal: principal, Bearer: fmt.Sprintf("one-time-bearer-%d", service.bearerSerial)}, nil
}
func (service *fakePrincipalService) RevokeCredential(_ context.Context, id, revision string) (contract.Principal, error) {
	if service.err != nil {
		return contract.Principal{}, service.err
	}
	principal, err := service.credentialPrincipal(id, revision)
	if err != nil {
		return contract.Principal{}, err
	}
	if principal.Credential == nil {
		return contract.Principal{}, authorization.ErrConflict
	}
	value, _ := strconv.Atoi(principal.Revision)
	credentialValue, _ := strconv.Atoi(principal.CredentialRevision)
	principal.Revision = strconv.Itoa(value + 1)
	principal.CredentialRevision = strconv.Itoa(credentialValue + 1)
	principal.Credential = nil
	service.items[0] = principal
	return principal, nil
}
func (service *fakePrincipalService) credentialPrincipal(id, revision string) (contract.Principal, error) {
	if len(service.items) == 0 || service.items[0].ID != id {
		return contract.Principal{}, authorization.ErrNotFound
	}
	if revision != service.items[0].Revision {
		return contract.Principal{}, authorization.ErrStaleRevision
	}
	return service.items[0], nil
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

func (service *fakePrincipalService) CreateGrant(_ context.Context, request authorization.CreateGrantRequest, validate authorization.CurrentGrantTargetValidator) (contract.Grant, error) {
	if service.err != nil {
		return contract.Grant{}, service.err
	}
	if validate == nil {
		return contract.Grant{}, authorization.ErrInvalidInput
	}
	valid, err := validate(context.Background(), nil, request.ServerID)
	if err != nil {
		return contract.Grant{}, err
	}
	if !valid {
		return contract.Grant{}, authorization.ErrInvalidInput
	}
	service.grantCreate = request
	grant := contract.Grant{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", PrincipalID: request.PrincipalID, Effect: request.Effect, ServerID: request.ServerID, UpstreamName: request.UpstreamName, Constraint: request.Constraint, State: contract.GrantActive, CreatedAt: "2026-08-25T00:00:00Z"}
	if request.ExpiresAt != nil {
		value := request.ExpiresAt.UTC().Format(time.RFC3339Nano)
		grant.ExpiresAt = &value
	}
	service.grants = append(service.grants, grant)
	return grant, nil
}
func (service *fakePrincipalService) GetGrant(_ context.Context, id string) (contract.Grant, error) {
	if service.err != nil {
		return contract.Grant{}, service.err
	}
	for _, grant := range service.grants {
		if grant.ID == id {
			return grant, nil
		}
	}
	return contract.Grant{}, authorization.ErrNotFound
}
func (service *fakePrincipalService) ListGrants(_ context.Context, filter authorization.GrantFilter, cursor *authorization.SnapshotCursor, limit int) (authorization.GrantPage, error) {
	if service.err != nil {
		return authorization.GrantPage{}, service.err
	}
	service.grantFilter, service.grantCursor, service.limit = filter, cursor, limit
	page := authorization.GrantPage{Items: append([]contract.Grant(nil), service.grants...)}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = &authorization.SnapshotCursor{Collection: "grants", PrincipalID: filter.PrincipalID, ServerID: filter.ServerID, Upper: 2, After: 1, AfterID: page.Items[len(page.Items)-1].ID}
	}
	return page, nil
}
func (service *fakePrincipalService) DeleteGrant(_ context.Context, id string) error {
	if service.err != nil {
		return service.err
	}
	for index, grant := range service.grants {
		if grant.ID == id {
			service.grants = append(service.grants[:index], service.grants[index+1:]...)
			return nil
		}
	}
	return authorization.ErrNotFound
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

func TestPrincipalCredentialIssueReplaceAndRevoke(t *testing.T) {
	service := &fakePrincipalService{items: []contract.Principal{principalResource()}}
	var invalidations []contract.Invalidation
	handler := newPrincipalHandler(t, service, &invalidations)
	request := func(method, revision string) *httptest.ResponseRecorder {
		return perform(handler, method, "/api/v1/principals/"+testID+"/credential", `{}`, map[string]string{
			"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON,
			"If-Match": contract.PrincipalETag(testID, revision),
		})
	}
	first := request(http.MethodPost, "1")
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "2"), first.Header().Get("ETag"))
	assert.Contains(t, first.Body.String(), `"bearer":"one-time-bearer-1"`)
	assert.Contains(t, first.Body.String(), `"credential":{"id":`)

	replaced := request(http.MethodPost, "2")
	require.Equal(t, http.StatusCreated, replaced.Code, replaced.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "3"), replaced.Header().Get("ETag"))
	assert.Contains(t, replaced.Body.String(), `"bearer":"one-time-bearer-2"`)
	assert.NotContains(t, replaced.Body.String(), "one-time-bearer-1")

	got := perform(handler, http.MethodGet, "/api/v1/principals/"+testID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.NotContains(t, got.Body.String(), "one-time-bearer")

	revoked := request(http.MethodDelete, "3")
	require.Equal(t, http.StatusOK, revoked.Code, revoked.Body.String())
	assert.Equal(t, contract.PrincipalETag(testID, "4"), revoked.Header().Get("ETag"))
	assert.Contains(t, revoked.Body.String(), `"credential":null`)
	assert.NotContains(t, revoked.Body.String(), "one-time-bearer")
	assert.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus},
		{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus},
		{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus},
	}, invalidations)
}

func TestPrincipalCredentialValidationAndErrors(t *testing.T) {
	service := &fakePrincipalService{items: []contract.Principal{principalResource()}}
	var invalidations []contract.Invalidation
	handler := newPrincipalHandler(t, service, &invalidations)
	authJSON := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, test := range []struct {
		name, method, body string
		headers            map[string]string
		status             int
		code               string
	}{
		{"nonempty issue", http.MethodPost, `{"extra":true}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")}, 400, "invalid_json"},
		{"missing precondition", http.MethodPost, `{}`, authJSON, 428, "principal_precondition_required"},
		{"weak precondition", http.MethodDelete, `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": "W/" + contract.PrincipalETag(testID, "1")}, 412, "stale_principal_revision"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := perform(handler, test.method, "/api/v1/principals/"+testID+"/credential", test.body, test.headers)
			assert.Equal(t, test.status, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), test.code)
		})
	}
	assert.Empty(t, invalidations)
	service.err = authorization.ErrConflict
	conflict := perform(handler, http.MethodPost, "/api/v1/principals/"+testID+"/credential", `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")})
	assert.Equal(t, http.StatusConflict, conflict.Code)
	service.err = authorization.ErrStorageUnavailable
	unavailable := perform(handler, http.MethodPost, "/api/v1/principals/"+testID+"/credential", `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "If-Match": contract.PrincipalETag(testID, "1")})
	assert.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
	assert.Contains(t, unavailable.Body.String(), "authorization_unavailable")
	assert.Empty(t, invalidations)
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
