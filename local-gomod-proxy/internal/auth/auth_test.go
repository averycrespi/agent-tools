package auth

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
})

var testCreds = state.Credentials{Username: "x", Password: "s3cret-token"}

func TestMiddleware_ValidCredsPassThrough(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", "s3cret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestMiddleware_WrongPassword_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", "wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="local-gomod-proxy"`, w.Header().Get("WWW-Authenticate"))
}

func TestMiddleware_WrongUsername_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "s3cret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_MissingHeader_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="local-gomod-proxy"`, w.Header().Get("WWW-Authenticate"))
}

func TestMiddleware_MalformedHeader_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer totally-not-basic")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_NeverLogsAuthorizationValue(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := Middleware(ok, testCreds)

	secret := "this-exact-secret-must-not-appear-in-logs"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	assert.NotContains(t, logBuf.String(), secret)
	assert.NotContains(t, logBuf.String(), "Authorization")
}
