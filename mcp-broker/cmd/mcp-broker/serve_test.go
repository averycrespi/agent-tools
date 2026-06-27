package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingShutdownServer struct {
	closed bool
}

func (s *blockingShutdownServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingShutdownServer) Close() error {
	s.closed = true
	return nil
}

func TestHandleHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	handleHealthz(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "ok\n", rec.Body.String())
}

func TestLimitRequestBodyRejectsOversizedContentLength(t *testing.T) {
	called := false
	handler := limitRequestBody(4, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.False(t, called)
}

func TestLimitRequestBodyAllowsWithinLimit(t *testing.T) {
	handler := limitRequestBody(4, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "1234", string(body))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("1234"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestLimitRequestBodyDisabledWhenZero(t *testing.T) {
	handler := limitRequestBody(0, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "12345", string(body))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestShutdownServerForcesCloseAfterTimeout(t *testing.T) {
	srv := &blockingShutdownServer{}

	start := time.Now()
	err := shutdownServer(srv, slog.Default(), 10*time.Millisecond)

	require.NoError(t, err)
	require.True(t, srv.closed)
	require.Less(t, time.Since(start), time.Second)
}
