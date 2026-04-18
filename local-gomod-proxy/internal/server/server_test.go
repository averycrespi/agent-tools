package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
	err error
}

func (s *stubRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return s.out, s.err
}

func TestHandler_PublicRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "public-response")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	h := New(router.New("github.com/private/*"), private.New(&stubRunner{}), public.New(u))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rsc.io/quote/@v/list", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "public-response", w.Body.String())
}

func TestHandler_PrivateRoute(t *testing.T) {
	runner := &stubRunner{out: []byte(`{"Versions":["v1.0.0"]}`)}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")), // should never be hit
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/list", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "v1.0.0")
}

func TestHandler_PrivateRoute_BadGateway(t *testing.T) {
	runner := &stubRunner{err: fmt.Errorf("boom")}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")), // should never be hit
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/v1.0.0.info", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandler_PrivateRoute_NotFound(t *testing.T) {
	// go mod download reports a missing version — server maps to 404 so the
	// Go client gets the expected "not found" signal instead of a 502 retry.
	runner := &stubRunner{
		out: []byte(`{"Error":"github.com/private/repo@v99.0.0: invalid version: unknown revision v99.0.0"}`),
	}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")),
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/v99.0.0.info", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "unknown revision")
}

func TestHandler_BadPath(t *testing.T) {
	h := New(router.New(""), nil, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/not-a-module", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}
