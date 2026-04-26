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
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mcp-broker")
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
	Name         string
	Description  string
	Response     string // JSON text returned by CallTool
	Annotations  *gomcp.ToolAnnotation
	OutputSchema *gomcp.ToolOutputSchema
	Meta         *gomcp.Meta
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
				return gomcp.NewToolResultText(td.Response), nil
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
	Rules       []testRuleConfig            `json:"rules"`
	Port        int                         `json:"port"`
	OpenBrowser bool                        `json:"open_browser"`
	Audit       testAuditConfig             `json:"audit"`
	Log         testLogConfig               `json:"log"`
}

type testServerConfig struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url"`
}

type testRuleConfig struct {
	Tool    string `json:"tool"`
	Verdict string `json:"verdict"`
}

type testAuditConfig struct {
	Path string `json:"path"`
}

type testLogConfig struct {
	Level string `json:"level"`
}

// --- TestStack ---

type TestStack struct {
	BrokerURL string
	AuthToken string
	Client    *client.Client
	t         *testing.T
}

type stackOpts struct {
	Tools []toolDef
	Rules []testRuleConfig
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

	// Write temp config.
	tmpDir := t.TempDir()
	cfg := testConfig{
		Servers: map[string]testServerConfig{
			"echo": {Type: "streamable-http", URL: backendURL},
		},
		Rules:       rules,
		Port:        brokerPort,
		OpenBrowser: false,
		Audit:       testAuditConfig{Path: filepath.Join(tmpDir, "audit.db")},
		Log:         testLogConfig{Level: "debug"},
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, cfgData, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Start broker subprocess.
	brokerCmd := exec.Command(brokerBinary, "serve", "--config", cfgPath)
	brokerCmd.Stdout = os.Stdout
	brokerCmd.Stderr = os.Stderr
	// Set XDG_CONFIG_HOME so the broker writes the auth token to a known location.
	brokerCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpDir)
	if err := brokerCmd.Start(); err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() {
		_ = brokerCmd.Process.Signal(os.Interrupt)
		_ = brokerCmd.Wait()
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

	// Read the auto-generated auth token.
	tokenData, err := os.ReadFile(filepath.Join(tmpDir, "mcp-broker", "auth-token"))
	if err != nil {
		t.Fatalf("read auth token: %v", err)
	}
	authToken := string(tokenData)

	// Connect MCP client.
	mcpClient, err := client.NewStreamableHttpClient(brokerURL+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + authToken,
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

	return &TestStack{
		BrokerURL: brokerURL,
		AuthToken: authToken,
		Client:    mcpClient,
		t:         t,
	}
}

// --- Dashboard API helpers ---

// pendingResponse is the JSON shape returned by GET /api/pending.
type pendingResponse []struct {
	ID   string `json:"id"`
	Tool string `json:"tool"`
}

func (s *TestStack) getPending() pendingResponse {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.BrokerURL+"/dashboard/api/pending", nil)
	req.Header.Set("Authorization", "Bearer "+s.AuthToken)
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
	body, _ := json.Marshal(map[string]string{"id": id, "decision": decision})
	req, _ := http.NewRequest("POST", s.BrokerURL+"/dashboard/api/decide", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.AuthToken)
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

func (s *TestStack) approve(id string) { s.decide(id, "approve") }
func (s *TestStack) deny(id string)    { s.decide(id, "deny") }

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
	req.Header.Set("Authorization", "Bearer "+s.AuthToken)
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
	Tool     string `json:"tool"`
	Verdict  string `json:"verdict"`
	Approved *bool  `json:"approved,omitempty"`
	Error    string `json:"error,omitempty"`
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
	req.Header.Set("Authorization", "Bearer "+s.AuthToken)
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
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.Client.CallTool(ctx, req)
}
