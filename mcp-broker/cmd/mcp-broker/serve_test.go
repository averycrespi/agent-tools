package main

import (
	"context"
	"encoding/json"
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

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type blockingShutdownServer struct {
	closed bool
}

type recordingServer struct {
	shutdowns int
	closes    int
}

type controllableShutdownServer struct {
	started chan struct{}
	release chan struct{}
}

type reloadTestManager struct {
	tools []server.Tool
	calls int
}

type reloadTestAudit struct {
	records []audit.Record
}

func (s *recordingServer) Shutdown(context.Context) error {
	s.shutdowns++
	return nil
}

func (s *recordingServer) Close() error {
	s.closes++
	return nil
}

func (s *controllableShutdownServer) Shutdown(context.Context) error {
	close(s.started)
	<-s.release
	return nil
}

func (s *controllableShutdownServer) Close() error {
	return nil
}

func (m *reloadTestManager) Tools() []server.Tool {
	return append([]server.Tool(nil), m.tools...)
}

func (m *reloadTestManager) Call(context.Context, string, map[string]any) (*server.ToolResult, error) {
	m.calls++
	return &server.ToolResult{Content: "ok"}, nil
}

func (a *reloadTestAudit) Record(_ context.Context, rec audit.Record) error {
	a.records = append(a.records, rec)
	return nil
}

func (a *reloadTestAudit) Query(context.Context, audit.QueryOpts) ([]audit.Record, int, error) {
	return nil, 0, nil
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

func TestReloadRulesFromConfigUpdatesSharedBrokerAndDashboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"rules": [{"tool": "github.*", "verdict": "allow", "reason": "reloaded"}]}`), 0o600))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)
	mgr := &reloadTestManager{tools: []server.Tool{{Name: "github.search"}}}
	auditor := &reloadTestAudit{}
	dash := dashboard.New(mgr, store, auditor, nil)
	dashServer := httptest.NewServer(dash.Handler())
	defer dashServer.Close()
	b := broker.New(mgr, store, auditor, nil, nil)

	require.NoError(t, reloadRulesFromConfig(path, store, nil))

	result, err := b.Handle(context.Background(), "github.search", nil)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
	require.Equal(t, 1, mgr.calls)

	resp, err := http.Get(dashServer.URL + "/api/rules")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Rules []struct {
			Tool    string `json:"tool"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"rules"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Rules, 1)
	require.Equal(t, "github.*", body.Rules[0].Tool)
	require.Equal(t, "allow", body.Rules[0].Verdict)
	require.Equal(t, "reloaded", body.Rules[0].Reason)
}

func TestReloadRulesFromConfigFailuresLeaveRulesActive(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "missing config",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.json") },
		},
		{
			name: "unreadable config path",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "config.json")
				require.NoError(t, os.Mkdir(path, 0o750))
				return path
			},
		},
		{
			name: "invalid json",
			path: func(t *testing.T) string { return writeReloadConfig(t, `{"rules": [`) },
		},
		{
			name: "malformed rules shape",
			path: func(t *testing.T) string { return writeReloadConfig(t, `{"rules": {"tool": "*", "verdict": "allow"}}`) },
		},
		{
			name: "malformed argument path",
			path: func(t *testing.T) string {
				return writeReloadConfig(t, `{"rules": [{"tool": "*", "verdict": "allow", "args": [{"path": "bad..path", "match": "value"}]}]}`)
			},
		},
		{
			name: "invalid regex",
			path: func(t *testing.T) string {
				return writeReloadConfig(t, `{"rules": [{"tool": "*", "verdict": "allow", "args": [{"path": "branch", "match": {"regex": "[invalid"}}]}]}`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
			require.NoError(t, err)

			err = reloadRulesFromConfig(tt.path(t), store, nil)
			require.Error(t, err)

			result := store.EvaluateWithMetadata("anything", nil)
			require.Equal(t, rules.Deny, result.Verdict)
			require.Equal(t, "old", result.RuleReason)
		})
	}
}

func writeReloadConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

func TestReloadRulesFromConfigDefaultsWhenRulesOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"port": 9000}`), 0o600))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})
	require.NoError(t, err)

	require.NoError(t, reloadRulesFromConfig(path, store, nil))

	result := store.EvaluateWithMetadata("anything", nil)
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

func TestServeEventLoopStopSignalShutsDown(t *testing.T) {
	stop := make(chan os.Signal, 1)
	reload := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	srv := &recordingServer{}

	done := make(chan error, 1)
	go func() {
		done <- serveEventLoop(stop, reload, errCh, srv, nil, func() error {
			return nil
		})
	}()

	stop <- syscall.SIGTERM
	require.NoError(t, <-done)
	require.Equal(t, 1, srv.shutdowns)
	require.Zero(t, srv.closes)
}

func TestServeEventLoopSecondStopSignalForcesExit(t *testing.T) {
	originalForceExit := forceExit
	exitCode := make(chan int, 1)
	forceExit = func(code int) { exitCode <- code }
	defer func() { forceExit = originalForceExit }()

	stop := make(chan os.Signal, 2)
	reload := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	srv := &controllableShutdownServer{started: make(chan struct{}), release: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- serveEventLoop(stop, reload, errCh, srv, nil, func() error {
			return nil
		})
	}()

	stop <- syscall.SIGINT
	<-srv.started
	stop <- syscall.SIGTERM
	require.Equal(t, 1, <-exitCode)
	close(srv.release)
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

func TestParseGrantHeader(t *testing.T) {
	tests := []struct {
		name      string
		headers   map[string][]string
		wantToken string
		wantErr   string
	}{
		{name: "absent"},
		{name: "single token", headers: map[string][]string{"Mcp-Broker-Grant": {"abc123"}}, wantToken: "abc123"},
		{name: "empty", headers: map[string][]string{"Mcp-Broker-Grant": {""}}, wantErr: "must not be empty"},
		{name: "duplicate", headers: map[string][]string{"Mcp-Broker-Grant": {"one", "two"}}, wantErr: "multiple"},
		{name: "comma combined", headers: map[string][]string{"Mcp-Broker-Grant": {"one,two"}}, wantErr: "comma-combined"},
		{name: "space separated", headers: map[string][]string{"Mcp-Broker-Grant": {"one two"}}, wantErr: "single token"},
		{name: "trimmed whitespace", headers: map[string][]string{"Mcp-Broker-Grant": {" abc123"}}, wantErr: "single token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			for key, values := range tt.headers {
				for _, value := range values {
					header.Add(key, value)
				}
			}
			token, errText := parseGrantHeader(header)
			require.Equal(t, tt.wantToken, token)
			if tt.wantErr == "" {
				require.Empty(t, errText)
			} else {
				require.Contains(t, errText, tt.wantErr)
			}
		})
	}
}
