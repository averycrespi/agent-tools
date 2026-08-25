package api

import (
	"context"
	"database/sql"
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

const testServerID = "01ARZ3NDEKTSV4RRFFQ69G5FB0"

func newGrantHandler(t *testing.T, service *fakePrincipalService, target authorization.CurrentGrantTargetValidator, invalidations *[]contract.Invalidation) http.Handler {
	t.Helper()
	handler := New(Options{
		Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{},
		Principals: service, GrantTarget: target, Invalidate: func(event contract.Invalidation) {
			if invalidations != nil {
				*invalidations = append(*invalidations, event)
			}
		},
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

func allowGrantTarget(context.Context, *sql.Tx, string) (bool, error) { return true, nil }

func TestGrantCreateListGetDeleteAndInvalidation(t *testing.T) {
	service := &fakePrincipalService{}
	var invalidations []contract.Invalidation
	handler := newGrantHandler(t, service, allowGrantTarget, &invalidations)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	created := perform(handler, http.MethodPost, "/api/v1/grants", `{"principal_id":"`+testID+`","effect":"deny","server_id":"`+testServerID+`","upstream_name":"danger","constraint":{"equals":{"/count":1.0}},"expires_at":"2027-08-25T00:00:00Z"}`, headers)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	assert.Empty(t, created.Header().Get("ETag"))
	assert.Empty(t, created.Header().Get("Location"))
	assert.Empty(t, created.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, created.Body.String(), `"constraint":{"equals":{"/count":1.0}}`)
	assert.Equal(t, authorization.GrantFilter{}, service.grantFilter)

	second := contract.Grant{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", PrincipalID: testID, Effect: contract.GrantAllow, ServerID: testServerID, State: contract.GrantExpired, CreatedAt: "2026-08-25T00:00:00Z"}
	service.grants = append(service.grants, second)
	listed := perform(handler, http.MethodGet, "/api/v1/grants?limit=1&principal_id="+testID+"&server_id="+testServerID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Contains(t, listed.Body.String(), `"next_cursor":"`)
	assert.Equal(t, authorization.GrantFilter{PrincipalID: testID, ServerID: testServerID}, service.grantFilter)
	var page contract.Collection[contract.Grant]
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
	require.NotNil(t, page.NextCursor)
	continued := perform(handler, http.MethodGet, "/api/v1/grants?limit=2&principal_id="+testID+"&server_id="+testServerID+"&cursor="+*page.NextCursor, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
	require.NotNil(t, service.grantCursor)
	assert.Equal(t, authorization.SnapshotCursor{Collection: "grants", PrincipalID: testID, ServerID: testServerID, Upper: 2, After: 1, AfterID: service.grants[0].ID}, *service.grantCursor)

	got := perform(handler, http.MethodGet, "/api/v1/grants/"+service.grants[0].ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.Empty(t, got.Header().Get("ETag"))
	deleted := perform(handler, http.MethodDelete, "/api/v1/grants/"+second.ID, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	assert.Empty(t, deleted.Body.String())
	assert.Equal(t, []contract.Invalidation{
		{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus},
		{Kind: contract.InvalidationAuthorization}, {Kind: contract.InvalidationSystemStatus},
	}, invalidations)
}

func TestGrantCreateRequiresAllMembersAndExactNullableShapes(t *testing.T) {
	service := &fakePrincipalService{}
	handler := newGrantHandler(t, service, allowGrantTarget, nil)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	valid := `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":null}`
	response := perform(handler, http.MethodPost, "/api/v1/grants", valid, headers)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	assert.Nil(t, service.grantCreate.UpstreamName)
	assert.Nil(t, service.grantCreate.Constraint)
	assert.Nil(t, service.grantCreate.ExpiresAt)

	for _, test := range []struct{ name, body, code string }{
		{"missing nullable", `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null}`, "invalid_grant"},
		{"null required", `{"principal_id":null,"effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":null}`, "invalid_grant"},
		{"wrong nullable type", `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":1,"constraint":null,"expires_at":null}`, "invalid_grant"},
		{"noncanonical expiry", `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":"2027-08-25T00:00:00+00:00"}`, "invalid_grant"},
		{"unknown member", valid[:len(valid)-1] + `,"extra":true}`, "invalid_json"},
		{"duplicate member", `{"principal_id":"` + testID + `","principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":null}`, "invalid_json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := perform(handler, http.MethodPost, "/api/v1/grants", test.body, headers)
			assert.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
			assert.Contains(t, result.Body.String(), test.code)
		})
	}
}

func TestGrantAuthenticationSessionAndTargetValidation(t *testing.T) {
	service := &fakePrincipalService{}
	handler := newGrantHandler(t, service, allowGrantTarget, nil)
	unauthenticated := perform(handler, http.MethodGet, "/api/v1/grants", "", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	body := `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":"not-yet-discovered","constraint":null,"expires_at":null}`
	session := perform(handler, http.MethodPost, "/api/v1/grants", body, map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusCreated, session.Code, session.Body.String())
	missingOrigin := perform(handler, http.MethodPost, "/api/v1/grants", body, map[string]string{"Cookie": contract.SessionCookieName + "=session", "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusForbidden, missingOrigin.Code)

	rejecting := newGrantHandler(t, &fakePrincipalService{}, func(context.Context, *sql.Tx, string) (bool, error) { return false, nil }, nil)
	invalidTarget := perform(rejecting, http.MethodPost, "/api/v1/grants", body, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON})
	assert.Equal(t, http.StatusBadRequest, invalidTarget.Code, invalidTarget.Body.String())
	assert.Contains(t, invalidTarget.Body.String(), "invalid_grant")
}

func TestGrantQueryValidationErrorsAndNoFailureInvalidation(t *testing.T) {
	service := &fakePrincipalService{}
	var invalidations []contract.Invalidation
	handler := newGrantHandler(t, service, allowGrantTarget, &invalidations)
	for _, path := range []string{
		"/api/v1/grants?unknown=x", "/api/v1/grants?limit=1&limit=2", "/api/v1/grants?principal_id=", "/api/v1/grants?server_id=null", "/api/v1/grants?cursor=%ZZ",
	} {
		response := perform(handler, http.MethodGet, path, "", map[string]string{"Authorization": "Bearer " + testBearer})
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), "malformed_request")
	}
	badCursor := perform(handler, http.MethodGet, "/api/v1/grants?cursor=abc", "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, http.StatusBadRequest, badCursor.Code)
	assert.Contains(t, badCursor.Body.String(), "invalid_cursor")

	for _, test := range []struct {
		err        error
		status     int
		code       string
		method     string
		path, body string
	}{
		{authorization.ErrInvalidInput, 400, "invalid_grant", http.MethodPost, "/api/v1/grants", `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":null}`},
		{authorization.ErrResourceLimit, 429, "resource_limit", http.MethodPost, "/api/v1/grants", `{"principal_id":"` + testID + `","effect":"allow","server_id":"` + testServerID + `","upstream_name":null,"constraint":null,"expires_at":null}`},
		{authorization.ErrStaleCursor, 409, "stale_cursor", http.MethodGet, "/api/v1/grants", ""},
		{authorization.ErrNotFound, 404, "not_found", http.MethodDelete, "/api/v1/grants/01ARZ3NDEKTSV4RRFFQ69G5FAY", ""},
		{authorization.ErrStorageUnavailable, 503, "authorization_unavailable", http.MethodGet, "/api/v1/grants", ""},
		{errors.New("foreign"), 503, "authorization_unavailable", http.MethodGet, "/api/v1/grants", ""},
	} {
		service.err = test.err
		headers := map[string]string{"Authorization": "Bearer " + testBearer}
		if test.body != "" {
			headers["Content-Type"] = contract.MediaTypeJSON
		}
		response := perform(handler, test.method, test.path, test.body, headers)
		assert.Equal(t, test.status, response.Code, test.err)
		assert.Contains(t, response.Body.String(), test.code, test.err)
	}
	assert.Empty(t, invalidations)
}
