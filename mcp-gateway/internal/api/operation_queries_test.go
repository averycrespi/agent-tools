package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

type operationQueryFixture struct {
	fakeServerService
	query  servers.OperationQuery
	cursor *servers.OperationQueryCursor
	limit  int
}

func (f *operationQueryFixture) ActiveOperations(context.Context, string) ([]servers.Operation, bool, error) {
	return []servers.Operation{}, false, nil
}
func (f *operationQueryFixture) QueryOperations(_ context.Context, id string, q servers.OperationQuery, c *servers.OperationQueryCursor, limit int) (servers.OperationQueryPage, error) {
	f.query, f.cursor, f.limit = q, c, limit
	return servers.OperationQueryPage{Items: []servers.Operation{}, Next: &servers.OperationQueryCursor{Server: id, Upper: 61, Position: 50}, CollectionRange: contract.CollectionRange{TotalCount: 61}}, nil
}
func TestOperationQueryWireValidationAndLegacyCompatibility(t *testing.T) {
	fixture := &operationQueryFixture{}
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Servers: fixture})
	get := func(query string, status int) []byte {
		response := perform(handler, http.MethodGet, "/api/v1/servers/"+testID+"/operations?"+query, "", map[string]string{"Authorization": "Bearer " + testBearer})
		require.Equal(t, status, response.Code, response.Body.String())
		return response.Body.Bytes()
	}
	require.JSONEq(t, `{"items":[],"next_cursor":null}`, string(get("limit=50", 200)))
	require.JSONEq(t, `{"items":[],"has_more":false}`, string(get("projection=active", 200)))
	var first contract.QueryCollection[contract.ServerOperation]
	require.NoError(t, json.Unmarshal(get("sort=started&direction=descending&action=retry&status=running&limit=17", 200), &first))
	require.Equal(t, servers.OperationQuery{Sort: "started", Direction: "descending", Action: "retry", Status: "running"}, fixture.query)
	require.Equal(t, 17, fixture.limit)
	require.Equal(t, 61, first.TotalCount)
	require.NotNil(t, first.NextCursor)
	require.LessOrEqual(t, len(*first.NextCursor), 512)
	get("sort=started&direction=descending&action=retry&status=running&cursor="+*first.NextCursor, 200)
	require.NotNil(t, fixture.cursor)
	require.Equal(t, 61, int(fixture.cursor.Upper))
	get("sort=status&cursor="+*first.NextCursor, 409)
	get("sort=created&direction=descending", 200)
	require.Equal(t, servers.OperationQuery{Sort: "created", Direction: "descending"}, fixture.query)
	get("sort=created&cursor="+*first.NextCursor, 409)
	for _, query := range []string{"projection=active&limit=2", "projection=other", "projection=active&status=running", "sort=unknown", "sort=started&limit=051", "sort=started&limit=51", "sort=started&limit=0", "status=unknown", "action=unknown", "sort=", "sort=status&sort=started", "direction=descending", "sort=status&cursor=", "sort=status&unknown=x"} {
		get(query, 400)
	}
	get("sort=status&cursor=not-base64", 400)
	contents, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(contents, &raw))
	raw["unknown"] = true
	contents, err = json.Marshal(raw)
	require.NoError(t, err)
	get("sort=started&cursor="+base64.RawURLEncoding.EncodeToString(contents), 400)
}
