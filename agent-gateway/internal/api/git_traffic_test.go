package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type gitTrafficReadFixture struct {
	queries []contract.GitTrafficQuery
	ids     []string
	item    contract.GitTrafficRecord
}

func (f *gitTrafficReadFixture) ListGit(_ context.Context, q contract.GitTrafficQuery) (contract.GitTrafficPage, error) {
	f.queries = append(f.queries, q)
	return contract.GitTrafficPage{Items: []contract.GitTrafficRecord{}}, nil
}
func (f *gitTrafficReadFixture) GetGit(_ context.Context, id string) (contract.GitTrafficRecord, error) {
	f.ids = append(f.ids, id)
	return f.item, nil
}
func TestGitTrafficProjectsBoundedRefEvidence(t *testing.T) {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	reader := &gitTrafficReadFixture{item: contract.GitTrafficRecord{Admission: contract.GitTrafficAdmission{ID: id, Operation: "push", Commands: 2, Allowed: true, RefEvidence: &contract.GitTrafficRefEvidence{State: "complete", Refs: []contract.GitTrafficRequestedRef{{Name: "refs/heads/main", Action: "update"}, {Name: "refs/tags/old", Action: "delete"}}}}, Completion: &contract.GitTrafficCompletion{ReportedResult: "reported_partial", RefOutcomes: []string{"ok", "ng"}}}}
	h := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, GitTraffic: reader})
	response := perform(h, http.MethodGet, "/api/v2/git/traffic/"+id, "", map[string]string{"Authorization": "Bearer " + testBearer})
	require.Equal(t, 200, response.Code)
	var actual contract.GitTrafficRecord
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
	require.Equal(t, reader.item, actual)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
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
