package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
)

type blockingShutdownServer struct {
	closed bool
}

type recordingServer struct {
	shutdowns int
	closes    int
}

func (s *recordingServer) Shutdown(context.Context) error {
	s.shutdowns++
	return nil
}

func (s *recordingServer) Close() error {
	s.closes++
	return nil
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

func TestReloadRulesFromConfigSwapsRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"rules": [{"tool": "github.*", "verdict": "allow"}]}`), 0o600))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)

	require.NoError(t, reloadRulesFromConfig(path, store, nil))

	result := store.Evaluate("github.search", nil)
	require.Equal(t, rules.Allow, result.Verdict)
	require.Equal(t, []config.RuleConfig{{Tool: "github.*", Verdict: "allow"}}, store.Rules())
}

func TestReloadRulesFromConfigFailureLeavesRulesActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"rules": [{"tool": "*", "verdict": "allow", "args": [{"path": "bad..path", "match": "value"}]}]}`), 0o600))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)

	err = reloadRulesFromConfig(path, store, nil)
	require.Error(t, err)

	result := store.Evaluate("anything", nil)
	require.Equal(t, rules.Deny, result.Verdict)
	require.Equal(t, "old", result.Rule.Reason)
}

func TestReloadRulesFromConfigDefaultsWhenRulesOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"port": 9000}`), 0o600))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})
	require.NoError(t, err)

	require.NoError(t, reloadRulesFromConfig(path, store, nil))

	result := store.Evaluate("anything", nil)
	require.Equal(t, rules.RequireApproval, result.Verdict)
	require.Equal(t, config.DefaultConfig().Rules, store.Rules())
}

func TestServeEventLoopReloadSignalDoesNotShutdown(t *testing.T) {
	stop := make(chan os.Signal, 1)
	reload := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	srv := &recordingServer{}
	reloaded := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		done <- serveEventLoop(stop, reload, errCh, srv, nil, func() error {
			reloaded <- struct{}{}
			return nil
		})
	}()

	reload <- syscall.SIGHUP
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("reload was not called")
	}
	require.Zero(t, srv.shutdowns)
	require.Zero(t, srv.closes)

	errCh <- http.ErrServerClosed
	require.NoError(t, <-done)
}

func TestShutdownServerForcesCloseAfterTimeout(t *testing.T) {
	srv := &blockingShutdownServer{}

	start := time.Now()
	err := shutdownServer(srv, slog.Default(), 10*time.Millisecond)

	require.NoError(t, err)
	require.True(t, srv.closed)
	require.Less(t, time.Since(start), time.Second)
}

func TestParseApprovalMode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want broker.ApprovalMode
	}{
		{name: "empty defaults to wait"},
		{name: "wait", raw: "wait"},
		{name: "reject", raw: "reject", want: broker.ApprovalModeReject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseApprovalMode(tt.raw)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseApprovalModeRejectsUnknownValue(t *testing.T) {
	_, err := parseApprovalMode("never")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported Mcp-Broker-Approval-Mode")
}
