package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	require.Equal(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pd", "auth-token"), TokenPath())
}

func TestEnsureTokenCreatesAndReusesToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pd", "auth-token")

	first, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, first, 64)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	second, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestRotateTokenReplacesExistingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pd", "auth-token")
	first, err := EnsureToken(path)
	require.NoError(t, err)

	second, err := RotateToken(path)
	require.NoError(t, err)
	require.Len(t, second, 64)
	require.NotEqual(t, first, second)

	loaded, err := LoadToken(path)
	require.NoError(t, err)
	require.Equal(t, second, loaded)
}

func TestMiddlewareRejectsDashboardRequestsWithoutAuth(t *testing.T) {
	handler := Middleware("secret", okHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/api/tasks", nil))

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/dashboard/unauthorized", rec.Header().Get("Location"))
}

func TestMiddlewareTokenURLSetsCookieAndRedirects(t *testing.T) {
	handler := Middleware("secret", okHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil))

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/dashboard/", rec.Header().Get("Location"))
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, CookieName, cookies[0].Name)
	require.Equal(t, "secret", cookies[0].Value)
	require.Equal(t, "/dashboard/", cookies[0].Path)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
}

func TestMiddlewareAcceptsDashboardCookie(t *testing.T) {
	handler := Middleware("secret", okHandler())
	req := httptest.NewRequest(http.MethodGet, "/dashboard/events", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "secret"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

func TestMiddlewareRejectsNonDashboardRequestsWithUnauthorized(t *testing.T) {
	handler := Middleware("secret", okHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.JSONEq(t, `{"error":"unauthorized"}`, rec.Body.String())
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}
