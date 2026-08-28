package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/invocation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6InvocationAPI(t *testing.T) {
	service := &fakeInvocationReader{
		page: contract.InvocationPage{Items: []contract.InvocationSummary{invocationSummaryFixture()}, NextCursor: stringPointer("next")},
		item: contract.Invocation{InvocationSummary: invocationSummaryFixture(), RedactedArguments: json.RawMessage(`{"safe":"<script>alert(1)</script>"}`)},
	}
	invalidations := 0
	handler := newInvocationHandler(t, service, func(contract.Invalidation) { invalidations++ })
	bearer := map[string]string{"Authorization": "Bearer " + testBearer}

	query := url.Values{
		"limit": {"1"}, "cursor": {"opaque"}, "principal_id": {testID}, "server_id": {testServerID},
		"requested_name": {"namespace.tool"}, "admission_class": {"evaluated"}, "decision": {"allow"}, "outcome": {"succeeded"},
	}
	listed := perform(handler, http.MethodGet, "/api/v1/invocations?"+query.Encode(), "", bearer)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	assert.Equal(t, "no-store", listed.Header().Get("Cache-Control"))
	assert.Empty(t, listed.Header().Get("Access-Control-Allow-Origin"))
	assert.NotContains(t, listed.Body.String(), "redacted_arguments")
	assert.Equal(t, 1, service.query.Limit)
	require.NotNil(t, service.query.Cursor)
	assert.Equal(t, "opaque", *service.query.Cursor)
	assert.Equal(t, testID, *service.query.Filters.PrincipalID)
	assert.Equal(t, testServerID, *service.query.Filters.ServerID)
	assert.Equal(t, "namespace.tool", *service.query.Filters.RequestedName)
	assert.Equal(t, contract.AdmissionEvaluated, *service.query.Filters.AdmissionClass)
	assert.Equal(t, contract.DecisionAllow, *service.query.Filters.Decision)
	assert.Equal(t, contract.InvocationOutcomeSucceeded, *service.query.Filters.Outcome)

	item := perform(handler, http.MethodGet, "/api/v1/invocations/"+service.item.ID, "", bearer)
	require.Equal(t, http.StatusOK, item.Code, item.Body.String())
	assert.Equal(t, service.item.ID, service.itemID)
	assert.Contains(t, item.Body.String(), `"redacted_arguments":{"safe":"\u003cscript\u003ealert(1)\u003c/script\u003e"}`)
	assert.NotContains(t, item.Body.String(), "<script>")
	assert.Equal(t, 0, invalidations, "invocation reads must emit no event")

	session := map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin}
	assert.Equal(t, http.StatusOK, perform(handler, http.MethodGet, "/api/v1/invocations", "", session).Code)
	assert.Equal(t, contract.AdminListPageDefault, service.query.Limit)
	assert.Equal(t, http.StatusUnauthorized, perform(handler, http.MethodGet, "/api/v1/invocations", "", nil).Code)
	assert.Equal(t, http.StatusForbidden, perform(handler, http.MethodGet, "/api/v1/invocations", "", map[string]string{"Cookie": contract.SessionCookieName + "=session"}).Code)

	for _, target := range []string{
		"/api/v1/invocations?unknown=x", "/api/v1/invocations?limit=1&limit=2", "/api/v1/invocations?cursor=",
		"/api/v1/invocations?principal_id=null", "/api/v1/invocations?limit=0", "/api/v1/invocations?limit=101",
		"/api/v1/invocations?admission_class=other", "/api/v1/invocations?decision=other", "/api/v1/invocations?outcome=other",
	} {
		response := perform(handler, http.MethodGet, target, "", bearer)
		assert.Equal(t, http.StatusBadRequest, response.Code, target+": "+response.Body.String())
		assert.Contains(t, response.Body.String(), "malformed_request", target)
	}
	tooLongCursor := perform(handler, http.MethodGet, "/api/v1/invocations?cursor="+strings.Repeat("x", 513), "", bearer)
	assert.Equal(t, http.StatusBadRequest, tooLongCursor.Code)
	assert.Contains(t, tooLongCursor.Body.String(), "invalid_cursor")
	assert.Equal(t, http.StatusBadRequest, perform(handler, http.MethodGet, "/api/v1/invocations", `{}`, bearer).Code)
	assert.Equal(t, http.StatusBadRequest, perform(handler, http.MethodGet, "/api/v1/invocations/"+service.item.ID+"?x=1", "", bearer).Code)
	assert.Equal(t, http.StatusBadRequest, perform(handler, http.MethodGet, "/api/v1/invocations/"+service.item.ID, `{}`, bearer).Code)
	assert.Equal(t, http.StatusNotFound, perform(handler, http.MethodGet, "/api/v1/invocations/"+service.item.ID+"/extra", "", bearer).Code)
	response := perform(handler, http.MethodPost, "/api/v1/invocations", "", bearer)
	assert.Equal(t, http.StatusMethodNotAllowed, response.Code)
	assert.Equal(t, http.MethodGet, response.Header().Get("Allow"))

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{invocation.ErrNotFound, 404, "not_found"},
		{invocation.ErrInvalidInput, 400, "malformed_request"},
		{invocation.ErrInvalidCursor, 400, "invalid_cursor"},
		{invocation.ErrStaleCursor, 409, "stale_cursor"},
		{invocation.ErrStorageUnavailable, 503, "storage_unavailable"},
		{invocation.ErrInvalidState, 503, "storage_unavailable"},
		{errors.New("foreign"), 503, "storage_unavailable"},
	} {
		service.err = test.err
		target := "/api/v1/invocations/" + service.item.ID
		if errors.Is(test.err, invocation.ErrInvalidCursor) || errors.Is(test.err, invocation.ErrStaleCursor) {
			target = "/api/v1/invocations?cursor=opaque"
		}
		response = perform(handler, http.MethodGet, target, "", bearer)
		assert.Equal(t, test.status, response.Code, test.err)
		assert.Contains(t, response.Body.String(), test.code, test.err)
	}
}

type fakeInvocationReader struct {
	page   contract.InvocationPage
	item   contract.Invocation
	query  contract.InvocationListQuery
	itemID string
	err    error
}

func (reader *fakeInvocationReader) List(_ context.Context, query contract.InvocationListQuery) (contract.InvocationPage, error) {
	reader.query = query
	return reader.page, reader.err
}

func (reader *fakeInvocationReader) Get(_ context.Context, id string) (contract.Invocation, error) {
	reader.itemID = id
	return reader.item, reader.err
}

func newInvocationHandler(t *testing.T, reader InvocationReader, invalidate func(contract.Invalidation)) http.Handler {
	t.Helper()
	handler := New(Options{
		Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{},
		Invocations: reader, Invalidate: invalidate,
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	return boundary
}

func invocationSummaryFixture() contract.InvocationSummary {
	requestedName := "namespace.tool"
	return contract.InvocationSummary{
		ID: testID, PrincipalID: testID, CredentialID: testServerID, CredentialFingerprint: "0123456789abcdef",
		CredentialRevision: "1", AdmittedAt: "2026-08-27T00:00:00Z", AdmissionClass: contract.AdmissionEvaluated,
		RequestedName: &requestedName, Outcome: contract.InvocationOutcome{Class: contract.InvocationOutcomeSucceeded, Basis: contract.InvocationBasisTerminal},
	}
}

func stringPointer(value string) *string { return &value }
