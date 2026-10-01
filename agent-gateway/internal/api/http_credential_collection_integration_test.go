//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationHTTPCredentialCollectionFiltersAndCursorBinding(t *testing.T) {
	handler, restart, _ := newHTTPCredentialIntegrationHandlerWithRestart(t, &httpCredentialBackend{items: map[string]string{}})
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for _, name := range []string{"Earlier needle", "Later ordinary"} {
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(httpCredentialCreateBody), &body))
		body["name"] = name
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		require.Equal(t, 201, perform(handler, http.MethodPost, "/api/v2/http/credentials", string(raw), headers).Code)
	}
	get := func(query string) contract.QueryCollection[contract.HTTPCredential] {
		result := perform(handler, http.MethodGet, "/api/v2/http/credentials?"+query, "", headers)
		require.Equal(t, 200, result.Code, result.Body.String())
		var page contract.QueryCollection[contract.HTTPCredential]
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &page))
		require.NotContains(t, result.Body.String(), "rotation-canary")
		return page
	}
	first := get("limit=1")
	require.Equal(t, 2, first.TotalCount)
	require.Equal(t, "Later ordinary", first.Items[0].Name)
	require.NotNil(t, first.NextCursor)
	matched := get("name=nedle&status=configured&limit=1")
	require.Equal(t, 1, matched.TotalCount)
	require.Equal(t, 0, matched.Offset)
	require.Equal(t, "Earlier needle", matched.Items[0].Name)
	second := get("limit=1&cursor=" + url.QueryEscape(*first.NextCursor))
	require.Equal(t, 1, second.Offset)
	require.Equal(t, "Earlier needle", second.Items[0].Name)
	incompatible := perform(handler, http.MethodGet, "/api/v2/http/credentials?name=ordinary&cursor="+url.QueryEscape(*first.NextCursor), "", headers)
	require.Equal(t, 409, incompatible.Code)
	require.Contains(t, incompatible.Body.String(), "stale_cursor")
	restartedResult := perform(restart(), http.MethodGet, "/api/v2/http/credentials?limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", headers)
	require.Equal(t, 409, restartedResult.Code, restartedResult.Body.String())
	require.Contains(t, restartedResult.Body.String(), "stale_cursor")
	for _, query := range []string{"name=", "name=x&name=y", "status=active", "recipe=" + url.QueryEscape(strings.Repeat("é", 129)), "boundary=a%0Ab", "secret=x", "sort=name", "cursor=bad", "cursor=a&cursor=b"} {
		result := perform(handler, http.MethodGet, "/api/v2/http/credentials?"+query, "", headers)
		require.Equal(t, 400, result.Code, query)
	}
}
