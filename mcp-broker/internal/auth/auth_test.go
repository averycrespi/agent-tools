package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMiddlewareAllowsPublicEndpointsWithoutAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(testAuthStore(t), inner)

	for _, path := range []string{"/healthz", "/dashboard/unauthorized"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		require.Equal(t, http.StatusOK, response.Code, path)
	}
}

func TestMiddlewareEnforcesStrictRouteRoles(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(testAuthStore(t), inner)

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "agent on MCP", path: "/mcp", token: testAgent, wantStatus: http.StatusOK},
		{name: "admin on MCP", path: "/mcp", token: testAdmin, wantStatus: http.StatusUnauthorized},
		{name: "admin on dashboard", path: "/dashboard/", token: testAdmin, wantStatus: http.StatusOK},
		{name: "agent on dashboard", path: "/dashboard/", token: testAgent, wantStatus: http.StatusFound},
		{name: "admin on root catch all", path: "/", token: testAdmin, wantStatus: http.StatusOK},
		{name: "agent on root catch all", path: "/", token: testAgent, wantStatus: http.StatusUnauthorized},
		{name: "admin on unknown catch all", path: "/unknown", token: testAdmin, wantStatus: http.StatusOK},
		{name: "agent on unknown catch all", path: "/unknown", token: testAgent, wantStatus: http.StatusUnauthorized},
		{name: "missing token on MCP", path: "/mcp", wantStatus: http.StatusUnauthorized},
		{name: "missing token on dashboard", path: "/dashboard/", wantStatus: http.StatusFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			require.Equal(t, tt.wantStatus, response.Code)
			if tt.wantStatus == http.StatusFound && tt.path == "/dashboard/" {
				require.Equal(t, "/dashboard/unauthorized", response.Header().Get("Location"))
			}
		})
	}
}

func TestMiddlewareRejectsAdminCookieOnMCP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(testAuthStore(t), inner)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testAdmin})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestMiddlewareSetsAdminCookieAndRedirectsWithoutQueryToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(testAuthStore(t), inner)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/?token="+testAdmin+"&foo=bar", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	require.Equal(t, http.StatusFound, response.Code)
	require.Equal(t, "/dashboard/?foo=bar", response.Header().Get("Location"))
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, cookieName, cookies[0].Name)
	require.Equal(t, testAdmin, cookies[0].Value)
	require.Equal(t, "/dashboard/", cookies[0].Path)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
}

func TestMiddlewareRejectsAgentQueryTokenForDashboard(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(testAuthStore(t), inner)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/?token="+testAgent, nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	require.Equal(t, http.StatusFound, response.Code)
	require.Equal(t, "/dashboard/unauthorized", response.Header().Get("Location"))
	require.Empty(t, response.Result().Cookies())
}

func TestMiddlewareReadsLiveStoreOnEveryRequest(t *testing.T) {
	store := testAuthStore(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store, inner)
	paths := testTokenPaths(t)
	writeTestToken(t, paths.Agent, testThird)
	writeTestToken(t, paths.Admin, testAdmin)

	result := store.Reload(paths)
	require.NoError(t, result.AgentErr)

	oldRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	oldRequest.Header.Set("Authorization", "Bearer "+testAgent)
	oldResponse := httptest.NewRecorder()
	handler.ServeHTTP(oldResponse, oldRequest)
	require.Equal(t, http.StatusUnauthorized, oldResponse.Code)

	newRequest := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	newRequest.Header.Set("Authorization", "Bearer "+testThird)
	newResponse := httptest.NewRecorder()
	handler.ServeHTTP(newResponse, newRequest)
	require.Equal(t, http.StatusOK, newResponse.Code)
}

func testAuthStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(TokenSet{Agent: testAgent, Admin: testAdmin})
	require.NoError(t, err)
	return store
}
