package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type apiCollectionTargets struct{}

func (apiCollectionTargets) GrantDisplayNamesTx(context.Context, *sql.Tx) (map[string]string, error) {
	return map[string]string{contract.SyntheticServerID: "Gateway self-service tools"}, nil
}

func TestAuthorizationCollectionAPIWith128Records(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.Initialize(context.Background(), owner, testID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := testutil.NewFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	var entropy []byte
	for i := 0; i < 512; i++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("collection-fixture-%d", i)))
		entropy = append(entropy, digest[:]...)
	}
	repository, err := authorization.New(store, clock, testutil.NewFakeEntropy(entropy))
	require.NoError(t, err)
	var last contract.PrincipalCreation
	for i := 0; i < 128; i++ {
		name := "Duplicate"
		if i == 127 {
			name = "Faraway Match"
		}
		last, err = repository.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{DisplayName: name, Visibility: contract.VisibilityAll})
		require.NoError(t, err)
	}
	service, err := authorization.NewCollectionService(repository, apiCollectionTargets{})
	require.NoError(t, err)
	handler := New(Options{Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: fakeSessions{}, Principals: repository, AuthorizationCollections: service})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)
	auth := map[string]string{"Authorization": "Bearer " + testBearer}
	get := func(path string, into any) {
		t.Helper()
		response := perform(boundary, http.MethodGet, path, "", auth)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), into))
	}
	var first, second, third contract.Collection[contract.Principal]
	get("/api/v1/principals?sort=name&limit=50", &first)
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.NextCursor)
	get("/api/v1/principals?sort=name&limit=50&cursor="+*first.NextCursor, &second)
	require.Len(t, second.Items, 50)
	get("/api/v1/principals?sort=name&limit=50&cursor="+*second.NextCursor, &third)
	require.Len(t, third.Items, 28)
	require.Nil(t, third.NextCursor)
	seen := map[string]bool{}
	for _, page := range []contract.Collection[contract.Principal]{first, second, third} {
		for _, item := range page.Items {
			require.False(t, seen[item.ID])
			seen[item.ID] = true
		}
	}
	var back contract.Collection[contract.Principal]
	get("/api/v1/principals?sort=name&limit=50&cursor="+*first.NextCursor, &back)
	require.Equal(t, second.Items, back.Items)
	var match contract.Collection[contract.Principal]
	get("/api/v1/principals?name=faraway&state=active&visibility=all", &match)
	require.Len(t, match.Items, 1)
	require.Equal(t, last.Principal.ID, match.Items[0].ID)
	var grants contract.Collection[contract.GrantTableItem]
	get("/api/v1/grants?representation=table&sort=principal&limit=50", &grants)
	require.Len(t, grants.Items, 50)
	require.NotNil(t, grants.NextCursor)
	require.Equal(t, "Gateway self-service tools", grants.Items[0].ServerDisplayName)
	var g2, g3 contract.Collection[contract.GrantTableItem]
	get("/api/v1/grants?representation=table&sort=principal&limit=50&cursor="+*grants.NextCursor, &g2)
	get("/api/v1/grants?representation=table&sort=principal&limit=50&cursor="+*g2.NextCursor, &g3)
	require.Len(t, g2.Items, 50)
	require.Len(t, g3.Items, 28)
	require.Nil(t, g3.NextCursor)
	var grantMatch contract.Collection[contract.GrantTableItem]
	get("/api/v1/grants?representation=table&principal=faraway&identity=Default&effect=allow&state=active&target=Gateway", &grantMatch)
	require.Len(t, grantMatch.Items, 1)
	require.Equal(t, last.Principal.ID, grantMatch.Items[0].Grant.PrincipalID)
	var legacy contract.Collection[contract.Grant]
	get("/api/v1/grants", &legacy)
	require.Len(t, legacy.Items, 50)
	require.NotEmpty(t, legacy.Items[0].ID)
	for _, collection := range []string{"principals", "grants"} {
		for _, direction := range []string{"ascending", "descending"} {
			t.Run(collection+"/"+direction, func(t *testing.T) {
				path := "/api/v1/" + collection + "?direction=" + direction
				response := perform(boundary, http.MethodGet, path, "", auth)
				require.Equal(t, http.StatusBadRequest, response.Code)
				response = perform(boundary, http.MethodGet, path+"&sort=id", "", auth)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			})
		}
	}
	for _, path := range []string{
		"/api/v1/principals?sort=unknown", "/api/v1/principals?direction=up", "/api/v1/principals?name=a&name=b",
		"/api/v1/principals?name=" + strings.Repeat("a", 257), "/api/v1/principals?name=%00", "/api/v1/principals?sort=name&foreign=x",
		"/api/v1/grants?representation=unknown", "/api/v1/grants?principal_id=wrong&sort=id", "/api/v1/grants?effect=unknown", "/api/v1/grants?target=",
	} {
		response := perform(boundary, http.MethodGet, path, "", auth)
		require.Equal(t, 400, response.Code, path+": "+response.Body.String())
	}
	for _, path := range []string{"/api/v1/principals?sort=id&cursor=" + *first.NextCursor, "/api/v1/grants?representation=table&sort=target&cursor=" + *grants.NextCursor} {
		response := perform(boundary, http.MethodGet, path, "", auth)
		require.Equal(t, 409, response.Code, response.Body.String())
	}
	clock.Advance(contract.AuthorizationCursorLifetime)
	response := perform(boundary, http.MethodGet, "/api/v1/principals?sort=name&cursor="+*first.NextCursor, "", auth)
	require.Equal(t, 409, response.Code, response.Body.String())
}
