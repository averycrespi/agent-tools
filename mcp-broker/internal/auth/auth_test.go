package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureToken_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64) // 32 bytes hex-encoded

	// File should exist with 0600 permissions.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// File contents should match returned token.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, token, string(data))
}

func TestEnsureToken_ReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token1, err := EnsureToken(path)
	require.NoError(t, err)

	token2, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, token1, token2)
}

func TestEnsureToken_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64)
}

func TestLoadToken_FailsIfFileMissing(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nonexistent"))
	require.Error(t, err)
}

func TestMiddleware_AllowsValidBearerHeader(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_RejectsMissingAuth_MCP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mcp")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_RejectsInvalidToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_AllowsValidCookie_Dashboard(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_SetsTokenCookieAndRedirects(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Don't follow redirects.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/dashboard/?token=" + token)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/", resp.Header.Get("Location"))

	// Should have set the cookie.
	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, cookieName, cookies[0].Name)
	require.Equal(t, token, cookies[0].Value)
	require.True(t, cookies[0].HttpOnly)
}

func TestMiddleware_RedirectsUnauthDashboard(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/dashboard/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/unauthorized", resp.Header.Get("Location"))
}

func TestMiddleware_AllowsUnauthorizedPage(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dashboard/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
