//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitInventoryQueryContracts(t *testing.T) {
	h := newGitIntegrationHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	principal := perform(h, http.MethodPost, "/api/v2/principals", `{"display_name":"Éléphant agent","visibility":"all"}`, headers)
	require.Equal(t, 201, principal.Code)
	var p contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(principal.Body.Bytes(), &p))
	var repos []contract.GitRepository
	for i, name := range []string{"Zulu", "Alpine", "Alpine"} {
		created := perform(h, http.MethodPost, "/api/v2/git/repositories", fmt.Sprintf(`{"name":%q,"url":"https://example.com/r%d","aliases":[],"credential_id":null}`, name, i), headers)
		require.Equal(t, 201, created.Code)
		var repo contract.GitRepository
		require.NoError(t, json.Unmarshal(created.Body.Bytes(), &repo))
		repos = append(repos, repo)
		grant := perform(h, http.MethodPost, "/api/v2/git/grants", fmt.Sprintf(`{"principal_id":%q,"repository_id":%q,"description":%q,"policy":{"version":1,"read":true,"refs":[]},"expires_at":null}`, p.Principal.ID, repo.ID, name), headers)
		require.Equal(t, 201, grant.Code)
	}
	path := "/api/v2/git/repositories"
	response := perform(h, http.MethodGet, path+"?limit=1&sort=name", "", headers)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var first contract.QueryCollection[contract.GitRepository]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &first))
	require.Equal(t, "Alpine", first.Items[0].Name)
	require.Equal(t, 3, first.TotalCount)
	require.NotNil(t, first.NextCursor)
	next := perform(h, http.MethodGet, path+"?limit=1&sort=name&direction=ascending&cursor="+url.QueryEscape(*first.NextCursor), "", headers)
	require.Equal(t, 200, next.Code)
	var second contract.QueryCollection[contract.GitRepository]
	require.NoError(t, json.Unmarshal(next.Body.Bytes(), &second))
	require.Equal(t, 1, second.Offset)
	require.Greater(t, second.Items[0].ID, first.Items[0].ID)
	for _, query := range []string{"?sort=name&direction=descending&cursor=", "?sort=id&cursor=", "?name=Alpine&sort=name&cursor="} {
		bad := perform(h, http.MethodGet, path+query+url.QueryEscape(*first.NextCursor), "", headers)
		require.Equal(t, 409, bad.Code, bad.Body.String())
		require.Contains(t, bad.Body.String(), "stale_cursor")
	}
	for _, bad := range []string{"?name=", "?name=null", "?name=a&name=b", "?name=" + strings.Repeat("a", 257), "?name=%0A", "?origin=example", "?sort=policy", "?direction=ascending", "?sort=name&direction=sideways", "?limit=01", "?limit=101", "?cursor=invalid"} {
		require.Equal(t, 400, perform(h, http.MethodGet, path+bad, "", headers).Code, bad)
	}
	filtered := perform(h, http.MethodGet, path+"?name=alpnie&destination=r1&sort=name", "", headers)
	require.Equal(t, 200, filtered.Code)
	var result contract.QueryCollection[contract.GitRepository]
	require.NoError(t, json.Unmarshal(filtered.Body.Bytes(), &result))
	require.Equal(t, 1, result.TotalCount)
	require.Equal(t, repos[1].ID, result.Items[0].ID)
	grants := perform(h, http.MethodGet, "/api/v2/git/grants?identity=alpnie&repository=alpnie&principal=elephnat&state=active&sort=description&limit=1", "", headers)
	require.Equal(t, 200, grants.Code, grants.Body.String())
	var gp contract.QueryCollection[contract.GitGrant]
	require.NoError(t, json.Unmarshal(grants.Body.Bytes(), &gp))
	require.Equal(t, 2, gp.TotalCount)
	require.NotNil(t, gp.NextCursor)
	_, err := json.Marshal(gp.Items[0])
	require.NoError(t, err)
	require.NotContains(t, grants.Body.String(), "principal_display_name")
	// Rename through the normal revision-guarded mutation; both inventory snapshots invalidate.
	repo := repos[1]
	headers["If-Match"] = gitETag("repository", repo.ID, repo.Revision)
	updated := perform(h, http.MethodPatch, path+"/"+repo.ID, fmt.Sprintf(`{"name":"Moved","url":%q,"aliases":[],"credential_id":null}`, repo.URL), headers)
	require.Equal(t, 200, updated.Code)
	stale := perform(h, http.MethodGet, path+"?sort=name&cursor="+url.QueryEscape(*first.NextCursor), "", headers)
	require.Equal(t, 409, stale.Code)
	stale = perform(h, http.MethodGet, "/api/v2/git/grants?identity=alpnie&repository=alpnie&principal=elephnat&state=active&sort=description&cursor="+url.QueryEscape(*gp.NextCursor), "", headers)
	require.Equal(t, 409, stale.Code)
}
