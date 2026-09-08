package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inventoryResponseRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type inventoryFixture struct {
	fakeServerService
	items []servers.Server
}

func (fixture *inventoryFixture) Inventory(_ context.Context, upper *int64) ([]servers.Server, int64, error) {
	watermark := int64(len(fixture.items))
	if upper != nil {
		watermark = *upper
	}
	items := make([]servers.Server, 0)
	for _, item := range fixture.items {
		if item.InsertionSequence <= watermark {
			items = append(items, item)
		}
	}
	return items, watermark, nil
}

type inventoryActiveFixture struct {
	fakeActiveCatalog
	counts map[string]int64
}

func (fixture *inventoryActiveFixture) Status(id string) catalog.ActiveStatus {
	return catalog.ActiveStatus{State: contract.ActiveCatalogCurrent, ToolCount: fixture.counts[id]}
}

func TestServerQueryGlobalPagesAndLiveInvalidation(t *testing.T) {
	fixture := &inventoryFixture{}
	active := &inventoryActiveFixture{counts: make(map[string]int64)}
	for index := 0; index < 60; index++ {
		item := storedServer(servers.Definition{Namespace: fmt.Sprintf("server_%02d", index), DisplayName: fmt.Sprintf("Server %02d", 59-index), Enabled: index < 32, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
		item.ID, item.InsertionSequence = fmt.Sprintf("%026d", index+1), int64(index+1)
		fixture.items = append(fixture.items, item)
		active.counts[item.ID] = int64(index % 3)
	}
	saturated := false
	runtime := contract.RuntimeActive
	options := Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: fixture, ActiveCatalog: active,
		RuntimeStatus: func(string) RuntimeStatus {
			return RuntimeStatus{State: runtime, CredentialState: contract.ServerCredentialReady}
		},
		DispatchStatus: func(string) contract.LimitStatus { return contract.LimitStatus{Limit: 4, Saturated: saturated} },
	}
	handler := New(options)
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	get := func(query string, status int) contract.Collection[inventoryResponseRow] {
		response := perform(boundary, http.MethodGet, "/api/v1/servers?"+query, "", map[string]string{"Authorization": "Bearer " + testBearer})
		require.Equal(t, status, response.Code, response.Body.String())
		var page contract.Collection[inventoryResponseRow]
		if status == 200 {
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		} else {
			assert.Contains(t, response.Body.String(), "stale_cursor")
		}
		return page
	}
	first := get("sort=name&limit=50", 200)
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.NextCursor)
	assert.Equal(t, "Server 00", first.Items[0].DisplayName)
	second := get("sort=name&limit=50&cursor="+url.QueryEscape(*first.NextCursor), 200)
	require.Len(t, second.Items, 10)
	assert.Nil(t, second.NextCursor)
	assert.Equal(t, "Server 50", second.Items[0].DisplayName)
	replay := get("sort=name&limit=50&cursor="+url.QueryEscape(*first.NextCursor), 200)
	assert.Equal(t, second.Items, replay.Items)
	match := get("name=Server%2059&namespace=server_00&status=Ready&sort=name", 200)
	require.Len(t, match.Items, 1)
	assert.Equal(t, "Server 59", match.Items[0].DisplayName)
	assert.Empty(t, get("name=missing&sort=name", 200).Items)
	for _, sortKey := range []string{"name", "id", "namespace", "status", "tools"} {
		query := "sort=" + sortKey + "&direction=descending&limit=17"
		var cursor *string
		ids := make(map[string]bool)
		for count := 0; count < 4; count++ {
			requestQuery := query
			if cursor != nil {
				requestQuery += "&cursor=" + url.QueryEscape(*cursor)
			}
			page := get(requestQuery, 200)
			for _, item := range page.Items {
				require.False(t, ids[item.ID])
				ids[item.ID] = true
			}
			cursor = page.NextCursor
		}
		assert.Len(t, ids, 60)
		assert.Nil(t, cursor)
	}
	originalCursor := "sort=name&limit=50&cursor=" + url.QueryEscape(*first.NextCursor)
	saturated = true
	get(originalCursor, 409)
	saturated = false
	runtime = contract.RuntimeAuthenticationRequired
	get(originalCursor, 409)
	runtime = contract.RuntimeActive
	active.counts[fixture.items[0].ID]++
	get(originalCursor, 409)
	active.counts[fixture.items[0].ID]--
	fixture.items[0].DisplayName = "Changed"
	get(originalCursor, 409)
	fixture.items[0].DisplayName = "Server 59"
	get("sort=tools&limit=50&cursor="+url.QueryEscape(*first.NextCursor), 409)
	restarted := New(options)
	restartBoundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: restarted.Authenticate, Next: restarted})
	require.NoError(t, err)
	response := perform(restartBoundary, http.MethodGet, "/api/v1/servers?"+originalCursor, "", map[string]string{"Authorization": "Bearer " + testBearer})
	assert.Equal(t, 409, response.Code)
}

func TestServerQuerySortOrder(t *testing.T) {
	fixture := &inventoryFixture{}
	active := &inventoryActiveFixture{counts: make(map[string]int64)}
	names := []string{"Beta", "alpha", "beta", "Gamma", "alpha", "gamma"}
	namespaces := []string{"z", "b", "a", "y", "c", "x"}
	counts := []int64{2, 0, 2, 1, 0, 1}
	for index, name := range names {
		item := storedServer(servers.Definition{Namespace: namespaces[index], DisplayName: name, Enabled: index%2 == 0, Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}})
		item.ID, item.InsertionSequence = fmt.Sprintf("%026d", index+1), int64(index+1)
		fixture.items = append(fixture.items, item)
		active.counts[item.ID] = counts[index]
	}
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: fixture, ActiveCatalog: active,
		RuntimeStatus: func(string) RuntimeStatus {
			return RuntimeStatus{State: contract.RuntimeActive, CredentialState: contract.ServerCredentialReady}
		},
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	for _, test := range []struct {
		key                   string
		ascending, descending []int
	}{
		{"name", []int{2, 5, 1, 3, 4, 6}, []int{4, 6, 1, 3, 2, 5}},
		{"id", []int{1, 2, 3, 4, 5, 6}, []int{6, 5, 4, 3, 2, 1}},
		{"namespace", []int{3, 2, 5, 6, 4, 1}, []int{1, 4, 6, 5, 2, 3}},
		{"status", []int{2, 4, 6, 1, 3, 5}, []int{1, 3, 5, 2, 4, 6}},
		{"tools", []int{2, 5, 4, 6, 1, 3}, []int{1, 3, 4, 6, 2, 5}},
	} {
		for direction, expected := range map[string][]int{"ascending": test.ascending, "descending": test.descending} {
			t.Run(test.key+"/"+direction, func(t *testing.T) {
				var cursor *string
				var ids []string
				for pageIndex := 0; pageIndex < 3; pageIndex++ {
					query := "sort=" + test.key + "&direction=" + direction + "&limit=2"
					if cursor != nil {
						query += "&cursor=" + url.QueryEscape(*cursor)
					}
					response := perform(boundary, http.MethodGet, "/api/v1/servers?"+query, "", map[string]string{"Authorization": "Bearer " + testBearer})
					require.Equal(t, 200, response.Code, response.Body.String())
					var page contract.Collection[inventoryResponseRow]
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
					require.Len(t, page.Items, 2)
					for _, item := range page.Items {
						ids = append(ids, item.ID)
					}
					cursor = page.NextCursor
				}
				assert.Nil(t, cursor)
				want := make([]string, len(expected))
				for index, value := range expected {
					want[index] = fmt.Sprintf("%026d", value)
				}
				assert.Equal(t, want, ids)
			})
		}
	}
}

func TestInventoryQueryStrictOptions(t *testing.T) {
	for _, raw := range []string{"name=", "name=x&name=y", "name=%00", "name=%FF", "status=active", "sort=unknown", "direction=ascending", "sort=name&direction=up", "sort=name&unknown=1", "sort=name&cursor=", "name=%ZZ"} {
		_, _, _, problem := parseInventoryQuery(raw)
		assert.Equal(t, contract.ProblemMalformedRequest, problem, raw)
	}
}
