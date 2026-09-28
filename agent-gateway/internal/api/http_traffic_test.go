package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type httpTrafficReader struct {
	query  contract.HTTPTrafficQuery
	id     string
	err    error
	page   contract.HTTPTrafficPage
	record contract.HTTPTrafficRecord
}

func (r *httpTrafficReader) ListHTTP(_ context.Context, q contract.HTTPTrafficQuery) (contract.HTTPTrafficPage, error) {
	r.query = q
	if r.page.Items != nil {
		return r.page, r.err
	}
	return contract.HTTPTrafficPage{Items: []contract.HTTPTrafficSummary{}}, r.err
}
func (r *httpTrafficReader) GetHTTP(_ context.Context, id string) (contract.HTTPTrafficRecord, error) {
	r.id = id
	return r.record, r.err
}
func TestHTTPTrafficReadAPI(t *testing.T) {
	reader := &httpTrafficReader{}
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, HTTPTraffic: reader})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: h.Authenticate, Next: h})
	require.NoError(t, err)
	bearer := map[string]string{"Authorization": "Bearer " + testBearer}
	session := map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin}
	path := "/api/v2/http/traffic"
	response := perform(handler, http.MethodGet, path+"?limit=1&principal_id="+testID+"&principal=Cafe&search_locale=en-US&destination=GiTHuB&type=request&decision=allow&outcome=succeeded&connect_id="+testID+"&cursor=opaque", "", bearer)
	require.Equal(t, 200, response.Code, response.Body.String())
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"items":[],"next_cursor":null}`, response.Body.String())
	assert.Equal(t, contract.HTTPTrafficQuery{Limit: 1, Cursor: "opaque", Filters: contract.HTTPTrafficFilters{ConnectID: testID, PrincipalID: testID, Principal: "Cafe", SearchLocale: "en-US", Destination: "GiTHuB", Type: "request", Decision: "allow", Outcome: "succeeded"}}, reader.query)
	for _, target := range []string{path, path + "/" + testID} {
		assert.Equal(t, 401, perform(handler, http.MethodGet, target, "", nil).Code)
		assert.Equal(t, 403, perform(handler, http.MethodGet, target, "", map[string]string{"Cookie": contract.SessionCookieName + "=session"}).Code)
		assert.Equal(t, 200, perform(handler, http.MethodGet, target, "", session).Code)
		assert.Equal(t, 400, perform(handler, http.MethodGet, target, `{}`, bearer).Code)
		assert.Equal(t, 405, perform(handler, http.MethodPost, target, "", bearer).Code)
	}
	assert.Equal(t, testID, reader.id)
	observed := &contract.HTTPTermination{Stage: "upstream_read", Condition: "failure", Context: "cancelled"}
	reader.record.Completion = &contract.HTTPTrafficCompletion{Outcome: "outcome_unknown", Status: 200, ResponseSource: "upstream", Termination: observed}
	reader.page.Items = []contract.HTTPTrafficSummary{{ID: testID, Outcome: "outcome_unknown", CompletionRecorded: true, Termination: observed}}
	for _, target := range []string{path, path + "/" + testID} {
		projected := perform(handler, http.MethodGet, target, "", bearer)
		require.Equal(t, 200, projected.Code)
		require.Equal(t, "no-store", projected.Header().Get("Cache-Control"))
		require.Contains(t, projected.Body.String(), `"termination":{"stage":"upstream_read","condition":"failure","context":"cancelled"}`)
	}
	reader.record.Completion = nil
	require.Contains(t, perform(handler, http.MethodGet, path+"/"+testID, "", bearer).Body.String(), `"completion":null`)
	for _, query := range []string{"?secret=x", "?limit=01", "?limit=101", "?limit=1&limit=2", "?destination=", "?type=request&type=connect", "?connect_id=", "?connect_id=a&connect_id=b", "?principal=", "?principal=a&principal=b", "?search_locale=", "?search_locale=en&search_locale=tr"} {
		assert.Equal(t, 400, perform(handler, http.MethodGet, path+query, "", bearer).Code, query)
	}
	assert.Equal(t, 400, perform(handler, http.MethodGet, path+"/"+testID+"?limit=1", "", bearer).Code)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{invocation.ErrStaleCursor, 409, "stale_cursor"}, {invocation.ErrInvalidCursor, 400, "invalid_cursor"}, {invocation.ErrTrafficCapacity, 503, "storage_unavailable"}, {invocation.ErrNotFound, 404, "not_found"}} {
		reader.err = tc.err
		response = perform(handler, http.MethodGet, path, "", bearer)
		assert.Equal(t, tc.status, response.Code, response.Body.String())
		assert.Contains(t, response.Body.String(), tc.code)
	}
}
