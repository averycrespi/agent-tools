//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

var brokerBinary string

func TestMain(m *testing.M) {
	// Build the broker binary once for all tests.
	tmp, err := os.MkdirTemp("", "mcp-broker-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "mcp-broker")
	cmd := exec.Command("go", "build", "-race", "-o", bin, "./cmd/mcp-broker")
	cmd.Dir = filepath.Join(mustFindModuleRoot(), "mcp-broker")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build mcp-broker: %v\n", err)
		os.Exit(1)
	}
	brokerBinary = bin

	os.Exit(m.Run())
}

// mustFindModuleRoot walks up from the working directory to find the go.work file.
func mustFindModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find go.work in any parent directory")
		}
		dir = parent
	}
}

// freePort returns a free TCP port by briefly binding to :0.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// --- Mock MCP backend ---

type toolDef struct {
	Name               string
	Description        string
	Response           string // JSON text returned by CallTool
	StructuredResponse any
	ResultIsError      bool
	CallFailure        bool
	Started            chan<- struct{}
	Release            <-chan struct{}
	Annotations        *gomcp.ToolAnnotation
	OutputSchema       *gomcp.ToolOutputSchema
	Meta               *gomcp.Meta
}

// startMockBackend starts an in-process mcp-go HTTP server with the given tools
// and returns the URL (e.g., "http://127.0.0.1:12345/mcp"). Cleanup is
// registered via t.Cleanup.
func startMockBackend(t *testing.T, tools []toolDef) string {
	t.Helper()

	srv := mcpserver.NewMCPServer("mock-backend", "0.1.0")
	for _, td := range tools {
		td := td
		tool := gomcp.Tool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: gomcp.ToolInputSchema{Type: "object"},
			Meta:        td.Meta,
		}
		if td.Annotations != nil {
			tool.Annotations = *td.Annotations
		}
		if td.OutputSchema != nil {
			tool.OutputSchema = *td.OutputSchema
		}
		srv.AddTool(
			tool,
			func(_ context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
				if td.Started != nil {
					td.Started <- struct{}{}
				}
				if td.Release != nil {
					<-td.Release
				}
				if td.CallFailure {
					return nil, fmt.Errorf("synthetic backend call failure")
				}
				var result *gomcp.CallToolResult
				if td.StructuredResponse != nil {
					result = gomcp.NewToolResultStructured(td.StructuredResponse, td.Response)
				} else {
					result = gomcp.NewToolResultText(td.Response)
				}
				result.IsError = td.ResultIsError
				return result, nil
			},
		)
	}

	handler := mcpserver.NewStreamableHTTPServer(srv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	httpSrv := &http.Server{Addr: addr, Handler: mux}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen mock backend: %v", err)
	}
	go func() { _ = httpSrv.Serve(l) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	return fmt.Sprintf("http://%s/mcp", addr)
}

// --- Config types (just enough to marshal config.json) ---

type testConfig struct {
	Servers     map[string]testServerConfig `json:"servers"`
	Rules       testRulesPathConfig         `json:"rules"`
	ToolPatches []testToolPatchConfig       `json:"tool_patches,omitempty"`
	Port        int                         `json:"port"`
	OpenBrowser bool                        `json:"open_browser"`
	Audit       testAuditConfig             `json:"audit"`
	Log         testLogConfig               `json:"log"`
	Hooks       *testHooksConfig            `json:"hooks,omitempty"`
}

type testServerConfig struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url"`
}

type testRulesPathConfig struct {
	Path string `json:"path"`
}

type testRulesConfig struct {
	Rules []testRuleConfig `json:"rules"`
}

type testRuleConfig struct {
	Tool    string           `json:"tool"`
	Verdict string           `json:"verdict"`
	Reason  string           `json:"reason,omitempty"`
	Args    []testArgPattern `json:"args,omitempty"`
}

type testArgPattern struct {
	Path  string          `json:"path"`
	Match json.RawMessage `json:"match"`
}

type testToolPatchConfig struct {
	Tool        string                    `json:"tool"`
	Disabled    bool                      `json:"disabled,omitempty"`
	Annotations *testToolAnnotationsPatch `json:"annotations,omitempty"`
}

type testToolAnnotationsPatch struct {
	Title           *string `json:"title,omitempty"`
	ReadOnlyHint    *bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool   `json:"openWorldHint,omitempty"`
}

type testAuditConfig struct {
	Path string `json:"path"`
}

type testLogConfig struct {
	Level string `json:"level"`
}

type testHooksConfig struct {
	Dispatch testHookDispatchConfig `json:"dispatch"`
	Events   testHookEventsConfig   `json:"events"`
}

type testHookDispatchConfig struct {
	MaxConcurrent   int   `json:"max_concurrent"`
	QueueSize       int   `json:"queue_size"`
	MaxPayloadBytes int64 `json:"max_payload_bytes"`
	MaxQueuedBytes  int64 `json:"max_queued_bytes"`
}

type testHookEventsConfig struct {
	RequireApproval []testHookHandlerConfig `json:"require-approval"`
}

type testHookHandlerConfig struct {
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Env            map[string]string `json:"env,omitempty"`
}

func testHooks(handlers ...testHookHandlerConfig) *testHooksConfig {
	return &testHooksConfig{
		Dispatch: testHookDispatchConfig{
			MaxConcurrent: 4, QueueSize: 64,
			MaxPayloadBytes: 10 * 1024 * 1024, MaxQueuedBytes: 64 * 1024 * 1024,
		},
		Events: testHookEventsConfig{RequireApproval: handlers},
	}
}

// --- TestStack ---

type TestStack struct {
	BrokerURL     string
	AgentToken    string
	AdminToken    string
	ConfigDir     string
	RulesPath     string
	OutputPath    string
	Client        *client.Client
	brokerCmd     *exec.Cmd
	brokerDone    <-chan error
	brokerStop    sync.Once
	brokerStopErr error
	t             *testing.T
}

func (s *TestStack) stopBroker(sig os.Signal, timeout time.Duration) error {
	s.brokerStop.Do(func() {
		select {
		case s.brokerStopErr = <-s.brokerDone:
			return
		default:
		}

		if err := s.brokerCmd.Process.Signal(sig); err != nil {
			s.brokerStopErr = err
			return
		}

		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case s.brokerStopErr = <-s.brokerDone:
		case <-timer.C:
			_ = s.brokerCmd.Process.Kill()
			<-s.brokerDone
			s.brokerStopErr = fmt.Errorf("broker did not exit within %s", timeout)
		}
	})
	return s.brokerStopErr
}

type stackOpts struct {
	Tools       []toolDef
	Rules       []testRuleConfig
	ToolPatches []testToolPatchConfig
	Hooks       *testHooksConfig
	LegacyToken string
}

func newTestStack(t *testing.T, opts stackOpts) *TestStack {
	t.Helper()

	// Default rules: require-approval for everything.
	rules := opts.Rules
	if len(rules) == 0 {
		rules = []testRuleConfig{{Tool: "*", Verdict: "require-approval"}}
	}

	// Start mock backend.
	backendURL := startMockBackend(t, opts.Tools)

	// Pick a free port for the broker.
	brokerPort := freePort(t)

	// Write temp config and separate rules file.
	tmpDir := t.TempDir()
	rulesPath := filepath.Join(tmpDir, "rules.json")
	cfg := testConfig{
		Servers: map[string]testServerConfig{
			"echo": {Type: "streamable-http", URL: backendURL},
		},
		Rules:       testRulesPathConfig{Path: rulesPath},
		ToolPatches: opts.ToolPatches,
		Port:        brokerPort,
		OpenBrowser: false,
		Audit:       testAuditConfig{Path: filepath.Join(tmpDir, "audit.db")},
		Log:         testLogConfig{Level: "debug"},
		Hooks:       opts.Hooks,
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, cfgData, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rulesData, err := json.MarshalIndent(testRulesConfig{Rules: rules}, "", "  ")
	if err != nil {
		t.Fatalf("marshal rules: %v", err)
	}
	if err := os.WriteFile(rulesPath, append(rulesData, '\n'), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	if opts.LegacyToken != "" {
		legacyPath := filepath.Join(tmpDir, "mcp-broker", "auth-token")
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0o750); err != nil {
			t.Fatalf("create legacy token directory: %v", err)
		}
		if err := os.WriteFile(legacyPath, []byte(opts.LegacyToken), 0o600); err != nil {
			t.Fatalf("write legacy token: %v", err)
		}
	}

	// Start broker subprocess.
	brokerCmd := exec.Command(brokerBinary, "serve", "--config", cfgPath)
	outputPath := filepath.Join(tmpDir, "broker-output.log")
	outputFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open broker output: %v", err)
	}
	t.Cleanup(func() { _ = outputFile.Close() })
	brokerCmd.Stdout = outputFile
	brokerCmd.Stderr = outputFile
	// Set XDG_CONFIG_HOME so the broker writes role credentials to a known location.
	brokerCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpDir)
	if err := brokerCmd.Start(); err != nil {
		t.Fatalf("start broker: %v", err)
	}
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- brokerCmd.Wait() }()
	stack := &TestStack{brokerCmd: brokerCmd, brokerDone: brokerDone, t: t}
	t.Cleanup(func() {
		if err := stack.stopBroker(os.Interrupt, 2*time.Second); err != nil {
			t.Logf("stop broker: %v", err)
		}
	})

	brokerURL := fmt.Sprintf("http://127.0.0.1:%d", brokerPort)

	// Wait for broker to be ready (poll the unauthenticated page).
	deadline := time.Now().Add(10 * time.Second)
	brokerReady := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(brokerURL + "/dashboard/unauthorized")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				brokerReady = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !brokerReady {
		t.Fatal("broker not ready within 10s")
	}

	// Read the auto-generated role credentials.
	agentData, err := os.ReadFile(filepath.Join(tmpDir, "mcp-broker", "agent-token"))
	if err != nil {
		t.Fatalf("read agent token: %v", err)
	}
	adminData, err := os.ReadFile(filepath.Join(tmpDir, "mcp-broker", "admin-token"))
	if err != nil {
		t.Fatalf("read admin token: %v", err)
	}
	agentToken := string(agentData)
	adminToken := string(adminData)

	// Connect MCP client with the agent credential.
	mcpClient, err := client.NewStreamableHttpClient(brokerURL+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + agentToken,
	}))
	if err != nil {
		t.Fatalf("create MCP client: %v", err)
	}
	t.Cleanup(func() { _ = mcpClient.Close() })

	// Initialize MCP session.
	initReq := gomcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = gomcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = gomcp.Implementation{Name: "e2e-test", Version: "0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := mcpClient.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize MCP client: %v", err)
	}

	stack.BrokerURL = brokerURL
	stack.AgentToken = agentToken
	stack.AdminToken = adminToken
	stack.ConfigDir = filepath.Join(tmpDir, "mcp-broker")
	stack.RulesPath = rulesPath
	stack.OutputPath = outputPath
	stack.Client = mcpClient
	return stack
}

// --- Dashboard API helpers ---

// pendingResponse is the JSON shape returned by GET /api/pending.
type pendingResponse []struct {
	ID        string    `json:"id"`
	Tool      string    `json:"tool"`
	Timestamp time.Time `json:"timestamp"`
}

func (s *TestStack) getPending() pendingResponse {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.BrokerURL+"/dashboard/api/pending", nil)
	req.Header.Set("Authorization", "Bearer "+s.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("get pending: %v", err)
	}
	defer resp.Body.Close()
	var items pendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		s.t.Fatalf("decode pending: %v", err)
	}
	return items
}

// waitForPending polls /api/pending until at least one item appears.
func (s *TestStack) waitForPending(timeout time.Duration) pendingResponse {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		items := s.getPending()
		if len(items) > 0 {
			return items
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatal("timed out waiting for pending request")
	return nil
}

func (s *TestStack) decide(id, decision string) {
	s.t.Helper()
	s.decideWithReason(id, decision, "")
}

func (s *TestStack) decideWithReason(id, decision, reason string) {
	s.t.Helper()
	payload := map[string]string{"id": id, "decision": decision}
	if reason != "" {
		payload["reason"] = reason
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", s.BrokerURL+"/dashboard/api/decide", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("decide: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		s.t.Fatalf("decide returned %d: %s", resp.StatusCode, b)
	}
}

func (s *TestStack) approve(id string)                { s.decide(id, "approve") }
func (s *TestStack) deny(id string)                   { s.decide(id, "deny") }
func (s *TestStack) denyWithReason(id, reason string) { s.decideWithReason(id, "deny", reason) }

// toolsResponse is the JSON shape returned by GET /api/tools.
type toolsResponse struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

func (s *TestStack) getTools() toolsResponse {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.BrokerURL+"/dashboard/api/tools", nil)
	req.Header.Set("Authorization", "Bearer "+s.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("get tools: %v", err)
	}
	defer resp.Body.Close()
	var result toolsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.t.Fatalf("decode tools: %v", err)
	}
	return result
}

// auditRecord matches the JSON shape of a single audit record from the API.
type auditRecord struct {
	Tool         string `json:"tool"`
	Verdict      string `json:"verdict"`
	Approved     *bool  `json:"approved,omitempty"`
	DenialReason string `json:"denial_reason,omitempty"`
	Error        string `json:"error,omitempty"`
}

type auditResponse struct {
	Records []auditRecord `json:"records"`
	Total   int           `json:"total"`
}

func (s *TestStack) getAudit(tool string, limit, offset int) auditResponse {
	s.t.Helper()
	url := fmt.Sprintf("%s/dashboard/api/audit?limit=%d&offset=%d", s.BrokerURL, limit, offset)
	if tool != "" {
		url += "&tool=" + tool
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+s.AdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("get audit: %v", err)
	}
	defer resp.Body.Close()
	var result auditResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.t.Fatalf("decode audit: %v", err)
	}
	return result
}

// callTool is a convenience wrapper for calling a tool via the MCP client.
func (s *TestStack) callTool(name string, args map[string]any) (*gomcp.CallToolResult, error) {
	return s.callToolWithHeaders(name, args, nil)
}

func (s *TestStack) callToolWithHeaders(name string, args map[string]any, headers http.Header) (*gomcp.CallToolResult, error) {
	req := gomcp.CallToolRequest{}
	req.Header = headers
	req.Params.Name = name
	req.Params.Arguments = args
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.Client.CallTool(ctx, req)
}
