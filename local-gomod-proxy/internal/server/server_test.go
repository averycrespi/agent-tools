package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

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

	h := New(router.New("github.com/private/*"), private.New(&stubRunner{}), public.New(u), 8)

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
		8,
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
		8,
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/v1.0.0.info", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandler_PrivateRoute_NotFound(t *testing.T) {
	// go mod download reports a missing version — server maps to 404 so the
	// Go client gets the expected "not found" signal instead of a 502 retry.
	// The response body must be a generic string: the underlying toolchain
	// output can contain host paths, usernames, and git-credential-helper
	// details that we don't want to leak to the sandboxed client.
	runner := &stubRunner{
		out: []byte(`{"Error":"github.com/private/repo@v99.0.0: invalid version: unknown revision v99.0.0 (from /Users/secret/go/pkg/mod/cache)"}`),
	}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")),
		8,
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/v99.0.0.info", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "module not found")
	assert.NotContains(t, body, "/Users/secret")
	assert.NotContains(t, body, "unknown revision")
}

func TestHandler_PrivateRoute_BadGateway_NoLeak(t *testing.T) {
	// Subprocess errors can contain filesystem paths, git credential-helper
	// output, or SSH diagnostics. None of it should reach the HTTP response.
	runner := &stubRunner{
		out: []byte("fatal: could not read Username for 'https://github.com': terminal prompts disabled\n/Users/secret/.netrc: bad syntax"),
		err: fmt.Errorf("exit status 1"),
	}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")),
		8,
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/v1.0.0.info", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "upstream error")
	assert.NotContains(t, body, "/Users/secret")
	assert.NotContains(t, body, ".netrc")
	assert.NotContains(t, body, "terminal prompts")
}

func TestHandler_BadPath(t *testing.T) {
	h := New(router.New(""), nil, nil, 8)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/not-a-module", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	// Only GET and HEAD are valid for the Go module proxy protocol. Reject
	// everything else at the handler edge so the public reverse proxy never
	// forwards non-spec methods with arbitrary bodies upstream.
	h := New(router.New(""), nil, nil, 8)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(m, "/rsc.io/quote/@v/list", nil))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method=%s", m)
		assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"), "method=%s", m)
	}
}

// blockingRunner holds every Run call until release is closed. It counts
// concurrent callers so tests can assert the semaphore cap is enforced.
type blockingRunner struct {
	inflight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	n := b.inflight.Add(1)
	defer b.inflight.Add(-1)
	for {
		peak := b.peak.Load()
		if n <= peak || b.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	select {
	case <-b.release:
		return []byte(`{"Versions":["v1.0.0"]}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestHandler_ConcurrencyCap(t *testing.T) {
	// With cap=2, fire 5 concurrent private requests and assert no more
	// than 2 ever sit inside the fetcher at once. The remaining 3 queue
	// on the semaphore until slots free.
	runner := &blockingRunner{release: make(chan struct{})}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")),
		2,
	)

	const concurrent = 5
	done := make(chan struct{}, concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/list", nil)
			h.ServeHTTP(w, req)
			done <- struct{}{}
		}()
	}

	// Give goroutines time to reach the semaphore + block in runner.
	require.Eventually(t, func() bool {
		return runner.inflight.Load() == 2
	}, time.Second, 10*time.Millisecond, "expected exactly 2 concurrent fetches")

	close(runner.release)
	for i := 0; i < concurrent; i++ {
		<-done
	}
	assert.Equal(t, int32(2), runner.peak.Load(), "peak concurrency exceeded cap")
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}
