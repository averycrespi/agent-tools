package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureTokenCreatesAndReusesToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-mailbox", "auth-token")
	first, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, first, 64)
	second, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestMiddlewareTokenURLSetsCookieAndRedirects(t *testing.T) {
	handler := Middleware("secret", okHandler())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret&status=new", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/dashboard/?status=new", rec.Header().Get("Location"))
	require.Len(t, rec.Result().Cookies(), 1)
	require.Equal(t, CookieName, rec.Result().Cookies()[0].Name)
	require.Equal(t, "/dashboard/", rec.Result().Cookies()[0].Path)
	require.True(t, rec.Result().Cookies()[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, rec.Result().Cookies()[0].SameSite)
}

func TestMiddlewareAcceptsBearerAndCookie(t *testing.T) {
	handler := Middleware("secret", okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/messages", nil)
	req.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/dashboard/events", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "secret"})
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareAllowsRootRedirectWithoutAuth(t *testing.T) {
	handler := Middleware("secret", okHandler())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareRejectsMissingAuth(t *testing.T) {
	handler := Middleware("secret", okHandler())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/api/messages", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/dashboard/unauthorized", rec.Header().Get("Location"))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
}
