package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	"github.com/stretchr/testify/require"
)

func TestListenUsesOnlyIPv4LoopbackAndValidatesPort(t *testing.T) {
	t.Parallel()

	listener, err := Listen(0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	addr := listener.Addr().(*net.TCPAddr)
	require.True(t, addr.IP.IsLoopback())
	require.Equal(t, "127.0.0.1", addr.IP.String())
	require.NotZero(t, addr.Port)

	for _, port := range []int{-1, 65536} {
		_, err = Listen(port)
		require.ErrorContains(t, err, "port must be between 0 and 65535")
	}
}

func TestHandlerAppliesPrivateHeadersAndServesEmbeddedAssets(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(NewHandler(nil))
	t.Cleanup(server.Close)
	for _, path := range []string{"/", "/assets/app.css", "/assets/app.js"} {
		response, err := http.Get(server.URL + path)
		require.NoError(t, err)
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		require.Contains(t, response.Header.Get("Content-Security-Policy"), "default-src 'self'")
		require.Contains(t, response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
		require.Equal(t, "no-referrer", response.Header.Get("Referrer-Policy"))
		require.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))
		require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
		require.Empty(t, response.Cookies())
		if path == "/" {
			require.Contains(t, string(body), "Private — not safe to share or screenshot")
			require.NotContains(t, string(body), "http://")
			require.NotContains(t, string(body), "https://")
		}
	}
}

func TestOverviewEndpointUsesValidatedCalendarParameters(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	started := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	require.NoError(t, db.ReplaceSession(context.Background(), ingest.Session{ID: "s", StartedAtUnix: &started}, store.SourceMeta{Path: "s.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, db.Close())
	boundary, err := robound.Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, boundary.Close()) })
	handler := NewHandler(boundary)
	handler.now = func() time.Time { return time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC) }

	request := httptest.NewRequest(http.MethodGet, "/api/overview?timezone=UTC&range=7d&bucket=auto", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var overview store.Overview
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &overview))
	require.Equal(t, store.BucketDay, overview.Unit)
	require.Len(t, overview.Buckets, 7)
	require.Equal(t, 1, overview.Buckets[6].Sessions)
	require.True(t, overview.Buckets[6].Partial)

	request = httptest.NewRequest(http.MethodGet, "/api/overview?timezone=UTC&range=90d&bucket=day", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), `"truncated":true`)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &overview))
	require.Len(t, overview.Buckets, 90)

	for _, timezone := range []string{"Not%2FAZone", "Local"} {
		request = httptest.NewRequest(http.MethodGet, "/api/overview?timezone="+timezone, nil)
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "timezone")
	}
}

func TestHandlerRejectsUnknownRoutesAndMethodsWithCappedJSON(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/missing", nil),
		httptest.NewRequest(http.MethodPost, "/", strings.NewReader("ignored")),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		require.LessOrEqual(t, recorder.Body.Len(), robound.MaxResponseBytes)
		var body map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.NotEmpty(t, body["error"])
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	}
}
