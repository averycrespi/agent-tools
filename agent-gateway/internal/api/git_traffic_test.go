package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type gitTrafficReadFixture struct {
	queries []contract.GitTrafficQuery
	ids     []string
}

func (f *gitTrafficReadFixture) ListGit(_ context.Context, q contract.GitTrafficQuery) (contract.GitTrafficPage, error) {
	f.queries = append(f.queries, q)
	return contract.GitTrafficPage{Items: []contract.GitTrafficRecord{}}, nil
}
func (f *gitTrafficReadFixture) GetGit(_ context.Context, id string) (contract.GitTrafficRecord, error) {
	f.ids = append(f.ids, id)
	return contract.GitTrafficRecord{}, nil
}
func TestGitTrafficRoutesAreReadOnlyBoundedAndBodyless(t *testing.T) {
	reader := &gitTrafficReadFixture{}
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, GitTraffic: reader})
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	response := perform(h, http.MethodGet, "/api/v2/git/traffic?limit=2&cursor=opaque", "", headers)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"items":[],"next_cursor":null}`, response.Body.String())
	require.Equal(t, []contract.GitTrafficQuery{{Limit: 2, Cursor: "opaque"}}, reader.queries)
	for _, path := range []string{"/api/v2/git/traffic?limit=101", "/api/v2/git/traffic?limit=01", "/api/v2/git/traffic?limit=1&limit=2", "/api/v2/git/traffic?unknown=secret", "/api/v2/git/traffic?cursor="} {
		require.Equal(t, 400, perform(h, http.MethodGet, path, "", headers).Code)
	}
	require.Equal(t, 400, perform(h, http.MethodGet, "/api/v2/git/traffic", "{}", headers).Code)
	require.Len(t, reader.queries, 1)
	require.Equal(t, 400, perform(h, http.MethodGet, "/api/v2/git/traffic/01ARZ3NDEKTSV4RRFFQ69G5FAV?secret=x", "", headers).Code)
	require.Empty(t, reader.ids)
	response = perform(h, http.MethodGet, "/api/v2/git/traffic?operation=push&repository=Recorded&admission=allowed&transport=complete&report=unknown&search_locale=en-US", "", headers)
	require.Equal(t, 200, response.Code)
	require.Equal(t, contract.GitTrafficFilters{Operation: "push", Repository: "Recorded", Admission: "allowed", Transport: "complete", Report: "unknown", SearchLocale: "en-US"}, reader.queries[1].Filters)
}
