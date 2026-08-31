package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/dashboard"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
)

type blockingShutdownServer struct {
	closed chan struct{}
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
	close(s.closed)
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

func TestRewriteLimaHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "lima host with port", host: "host.lima.internal:8200", want: "localhost:8200"},
		{name: "lima host without port", host: "host.lima.internal", want: "localhost"},
		{name: "lima host mixed case", host: "Host.Lima.Internal:8200", want: "localhost:8200"},
		{name: "localhost untouched", host: "localhost:8200", want: "localhost:8200"},
		{name: "loopback ip untouched", host: "127.0.0.1:8200", want: "127.0.0.1:8200"},
		{name: "other host untouched", host: "evil.example.com:8200", want: "evil.example.com:8200"},
		{name: "lima suffix not matched", host: "not-host.lima.internal:8200", want: "not-host.lima.internal:8200"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			handler := rewriteLimaHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Host
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusNoContent, rec.Code)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestRewriteLimaHostSatisfiesDNSRebindingGuard drives mcp-go's real guard over
// a loopback listener, which is the only way to prove the rewrite is what the
// guard accepts. The unwrapped case asserts the guard is still active, so this
// test fails loudly if a future mcp-go bump changes its shape.
func TestRewriteLimaHostSatisfiesDNSRebindingGuard(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`

	tests := []struct {
		name     string
		rewrite  bool
		wantCode int
	}{
		{name: "without middleware the guard rejects", rewrite: false, wantCode: http.StatusForbidden},
		{name: "with middleware the request is served", rewrite: true, wantCode: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var handler http.Handler = mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("test", "0.0.1"))
			if tt.rewrite {
				handler = rewriteLimaHost(handler)
			}
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(initialize))
			require.NoError(t, err)
			req.Host = limaInternalHost + ":8200"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")

			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, tt.wantCode, resp.StatusCode, "body: %s", body)
			if !tt.rewrite {
				require.Contains(t, string(body), "invalid Host header")
			}
		})
	}
}

func TestReloadRulesFromFileUpdatesSharedBrokerAndDashboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	require.NoError(t, config.SaveRulesFile(path, []config.RuleConfig{{Tool: "github.*", Verdict: "allow", Reason: "reloaded"}}))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
	require.NoError(t, err)
	mgr := &reloadTestManager{tools: []server.Tool{{Name: "github.search"}}}
	auditor := &reloadTestAudit{}
	dash := dashboard.New(mgr, store, auditor, nil)
	dashServer := httptest.NewServer(dash.Handler())
	defer dashServer.Close()
	b := broker.New(mgr, store, auditor, nil, nil)

	require.NoError(t, reloadRulesFromFile(path, store, nil))

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

func TestRunServeFailsClosedWhenStartupRulesDoNotCompile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data-home"))
	oldCfgFile := cfgFile
	cfgFile = filepath.Join(dir, "config.json")
	t.Cleanup(func() { cfgFile = oldCfgFile })

	rulesPath := filepath.Join(dir, "rules.json")
	cfg := config.DefaultConfigAt(cfgFile)
	cfg.Rules.Path = rulesPath
	cfg.OpenBrowser = false
	cfg.Audit.Path = filepath.Join(dir, "audit.db")
	cfg.Grants.Path = filepath.Join(dir, "grants.db")
	_, err := config.Save(cfg, cfgFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rulesPath, []byte(`{"rules":[{"tool":"*","verdict":"allow","args":[{"path":"bad..path","match":"value"}]}]}`), 0o600))

	err = runServe(serveCmd, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "compiling rules")
}

func TestReloadRulesFromFileFailuresLeaveRulesActive(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "missing rules file",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "rules.json") },
		},
		{
			name: "unreadable rules path",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "rules.json")
				require.NoError(t, os.Mkdir(path, 0o750))
				return path
			},
		},
		{
			name: "invalid json",
			path: func(t *testing.T) string { return writeReloadRulesFile(t, `{"rules": [`) },
		},
		{
			name: "malformed rules shape",
			path: func(t *testing.T) string {
				return writeReloadRulesFile(t, `{"rules": {"tool": "*", "verdict": "allow"}}`)
			},
		},
		{
			name: "malformed argument path",
			path: func(t *testing.T) string {
				return writeReloadRulesFile(t, `{"rules": [{"tool": "*", "verdict": "allow", "args": [{"path": "bad..path", "match": "value"}]}]}`)
			},
		},
		{
			name: "invalid regex",
			path: func(t *testing.T) string {
				return writeReloadRulesFile(t, `{"rules": [{"tool": "*", "verdict": "allow", "args": [{"path": "branch", "match": {"regex": "[invalid"}}]}]}`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny", Reason: "old"}})
			require.NoError(t, err)

			err = reloadRulesFromFile(tt.path(t), store, nil)
			require.Error(t, err)

			result := store.EvaluateWithMetadata("anything", nil)
			require.Equal(t, rules.Deny, result.Verdict)
			require.Equal(t, "old", result.RuleReason)
		})
	}
}

func writeReloadRulesFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

func TestReloadRulesFromFileLoadsDefaultRulesDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	require.NoError(t, config.SaveRulesFile(path, config.DefaultRules()))

	store, err := rules.NewStore([]config.RuleConfig{{Tool: "*", Verdict: "deny"}})
	require.NoError(t, err)

	require.NoError(t, reloadRulesFromFile(path, store, nil))

	result := store.EvaluateWithMetadata("anything", nil)
	require.Equal(t, rules.RequireApproval, result.Verdict)
	require.Equal(t, config.DefaultRules(), store.Rules())
}

func TestReloadBrokerStateAttemptsAuthWhenRulesReloadFails(t *testing.T) {
	paths := auth.TokenPaths{
		Agent:  filepath.Join(t.TempDir(), "agent-token"),
		Admin:  filepath.Join(t.TempDir(), "admin-token"),
		Legacy: filepath.Join(t.TempDir(), "auth-token"),
		Lock:   filepath.Join(t.TempDir(), ".token.lock"),
	}
	old := auth.TokenSet{Agent: strings.Repeat("a", 64), Admin: strings.Repeat("b", 64)}
	store, err := auth.NewStore(old)
	require.NoError(t, err)
	newAgent := strings.Repeat("c", 64)
	require.NoError(t, os.WriteFile(paths.Agent, []byte(newAgent), 0o600))
	require.NoError(t, os.WriteFile(paths.Admin, []byte(old.Admin), 0o600))

	err = reloadBrokerState(func() error { return errors.New("broken rules") }, store, paths, nil)

	require.ErrorContains(t, err, "broken rules")
	require.Equal(t, auth.TokenSet{Agent: newAgent, Admin: old.Admin}, store.Snapshot())
}

func TestAnnounceDashboardUsesAdminTokenOnlyForInteractivePTYAndOpening(t *testing.T) {
	admin := strings.Repeat("b", 64)
	master, terminal, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})
	require.True(t, isInteractiveOutput(terminal))
	var opened string

	announceDashboard(8080, admin, terminal, true, isInteractiveOutput, func(target string) error {
		opened = target
		return nil
	}, nil)

	buffer := make([]byte, 512)
	count, err := master.Read(buffer)
	require.NoError(t, err)
	output := string(buffer[:count])
	require.Contains(t, output, admin)
	require.Equal(t, strings.TrimPrefix(strings.TrimSpace(output), "Dashboard: "), opened)
}

func TestAnnounceDashboardIsSilentAndDoesNotOpenForNonInteractiveOutput(t *testing.T) {
	admin := strings.Repeat("b", 64)
	var output bytes.Buffer
	opened := false

	announceDashboard(8080, admin, &output, true, func(io.Writer) bool { return false }, func(string) error {
		opened = true
		return nil
	}, nil)

	require.Empty(t, output.String())
	require.False(t, opened)
}

func TestServeEventLoopReloadSignalDoesNotShutdown(t *testing.T) {
	stop := make(chan os.Signal, 1)
	reload := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	srv := &recordingServer{}
	reloaded := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		done <- serveEventLoop(stop, reload, errCh, nil, func() error {
			reloaded <- struct{}{}
			return nil
		}, func() error {
			return srv.Shutdown(context.Background())
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
		done <- serveEventLoop(stop, reload, errCh, nil, func() error {
			return nil
		}, func() error {
			return srv.Shutdown(context.Background())
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
		done <- serveEventLoop(stop, reload, errCh, nil, func() error {
			return nil
		}, func() error {
			return srv.Shutdown(context.Background())
		})
	}()

	stop <- syscall.SIGINT
	<-srv.started
	stop <- syscall.SIGTERM
	require.Equal(t, 1, <-exitCode)
	close(srv.release)
	require.NoError(t, <-done)
}

func TestShutdownApplicationForcesCloseWhenHTTPShutdownBlocks(t *testing.T) {
	_, cancelLifetime := context.WithCancel(context.Background())
	srv := &blockingShutdownServer{closed: make(chan struct{})}

	start := time.Now()
	err := shutdownApplication(cancelLifetime, srv, nil, nil, 10*time.Millisecond)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-srv.closed:
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not start")
	}
	require.Less(t, time.Since(start), time.Second)
}

func TestShutdownApplicationCancelsLifetimeBeforeHTTPShutdown(t *testing.T) {
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	srv := &recordingServer{}
	resourcesClosed := false

	err := shutdownApplication(cancelLifetime, srv, func(context.Context) error {
		select {
		case <-lifetimeCtx.Done():
			resourcesClosed = true
			return nil
		default:
			return errors.New("lifetime context was not canceled")
		}
	}, nil, time.Second)

	require.NoError(t, err)
	require.True(t, resourcesClosed)
	require.Equal(t, 1, srv.shutdowns)
}

func TestShutdownApplicationBoundsResourceCleanup(t *testing.T) {
	_, cancelLifetime := context.WithCancel(context.Background())
	srv := &recordingServer{}
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	done := make(chan error, 1)

	start := time.Now()
	go func() {
		done <- shutdownApplication(cancelLifetime, srv, func(context.Context) error {
			close(cleanupStarted)
			<-cleanupRelease
			return nil
		}, nil, 100*time.Millisecond)
	}()

	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		close(cleanupRelease)
		t.Fatal("resource cleanup did not start")
	}
	err := <-done
	close(cleanupRelease)

	require.ErrorIs(t, err, context.DeadlineExceeded)
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
