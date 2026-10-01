package api

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordedActivityReadBoundary(t *testing.T) {
	calls := 0
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, RecordedActivity: func() contract.RecordedActivitySummary {
		calls++
		return contract.RecordedActivitySummary{Coverage: "unavailable", EpochReason: "process_start", Buckets: []contract.RecordedActivityBucket{}}
	}})
	handler, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: h.Authenticate, Next: h})
	require.NoError(t, err)
	const path = "/api/v2/recorded-activity"
	bearer := map[string]string{"Authorization": "Bearer " + testBearer}
	session := map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin}
	for _, headers := range []map[string]string{bearer, session} {
		response := perform(handler, http.MethodGet, path, "", headers)
		require.Equal(t, 200, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		assert.Contains(t, response.Body.String(), `"coverage":"unavailable"`)
	}
	assert.Equal(t, 2, calls)
	for _, tc := range []struct {
		method, path, body string
		headers            map[string]string
		status             int
	}{
		{http.MethodGet, path, "", nil, 401},
		{http.MethodGet, path, "", map[string]string{"Cookie": contract.SessionCookieName + "=session"}, 403},
		{http.MethodPost, path, "", bearer, 405},
		{http.MethodHead, path, "", bearer, 405},
		{http.MethodGet, path, "{}", bearer, 400},
		{http.MethodGet, path + "?", "", bearer, 400},
		{http.MethodGet, path + "?limit=1", "", bearer, 400},
		{http.MethodGet, path + "?secret", "", bearer, 400},
	} {
		response := perform(handler, tc.method, tc.path, tc.body, tc.headers)
		assert.Equal(t, tc.status, response.Code, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
	assert.Equal(t, 2, calls, "rejected reads never access the observer")
	h.recordedActivity = nil
	response := perform(handler, http.MethodGet, path, "", bearer)
	assert.Equal(t, 503, response.Code)
	assert.NotContains(t, response.Body.String(), `"counts"`)
}
