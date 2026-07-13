package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureTokenCreatesPrivateFileAndReusesIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "auth-token")
	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, token, string(data))

	again, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, token, again)
}

func TestRotateReplacesToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "auth-token")
	first, err := EnsureToken(path)
	require.NoError(t, err)
	second, err := Rotate(path)
	require.NoError(t, err)
	require.Len(t, second, 64)
	require.NotEqual(t, first, second)
	loaded, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, second, loaded)
}

func TestTokenPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config-home")
	require.Equal(t, "/tmp/config-home/pi-session-analyzer/auth-token", TokenPath())
}

func TestChecksCompareTokens(t *testing.T) {
	t.Parallel()

	require.True(t, Equal("secret", "secret"))
	require.False(t, Equal("secret", "other"))

	bearer := httptest.NewRequest(http.MethodGet, "/", nil)
	bearer.Header.Set("Authorization", "Bearer secret")
	require.True(t, CheckBearer(bearer, "secret"))
	require.False(t, CheckBearer(bearer, "other"))
	require.False(t, CheckBearer(httptest.NewRequest(http.MethodGet, "/", nil), "secret"))

	withCookie := httptest.NewRequest(http.MethodGet, "/", nil)
	withCookie.AddCookie(&http.Cookie{Name: CookieName, Value: "secret"})
	require.True(t, CheckCookie(withCookie, "secret"))
	require.False(t, CheckCookie(withCookie, "other"))
	require.False(t, CheckCookie(httptest.NewRequest(http.MethodGet, "/", nil), "secret"))
}

func TestSetCookieIsHTTPOnlyStrictSessionCookie(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	SetCookie(recorder, "secret")
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, CookieName, cookies[0].Name)
	require.Equal(t, "secret", cookies[0].Value)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
	require.Equal(t, "/", cookies[0].Path)
	require.Positive(t, cookies[0].MaxAge)
}

func TestRedirectWithoutTokenPreservesOtherParameters(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?range=7d&token=secret&session=s1", nil)
	RedirectWithoutToken(recorder, request)
	require.Equal(t, http.StatusFound, recorder.Code)
	location := recorder.Header().Get("Location")
	require.NotContains(t, location, "secret")
	require.Contains(t, location, "range=7d")
	require.Contains(t, location, "session=s1")
}
