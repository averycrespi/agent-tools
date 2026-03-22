# MCP Broker E2E Tests Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add end-to-end tests that start the real mcp-broker binary as a subprocess, connect via MCP, exercise the approval flow through dashboard HTTP APIs, and verify tools listing and audit log behavior.

**Architecture:** Each test builds the broker binary once in `TestMain`, then spawns it as a subprocess with `--config` pointing at a temp directory. An in-process mcp-go HTTP server acts as the mock backend. The test connects to the broker as an MCP client and uses the dashboard REST API (`/api/pending`, `/api/decide`, `/api/tools`, `/api/audit`) for approval and verification.

**Tech Stack:** Go test, mcp-go (server + client), `os/exec` for subprocess, `net/http` for dashboard API calls, `testify/require` for assertions.

---

### Task 1: Create the TestStack helper and mock backend

**Files:**
- Create: `mcp-broker/test/e2e/teststack_test.go`

**Step 1: Write the TestStack helper**

This file contains `TestMain` (builds binary once), the mock MCP backend, the `TestStack` struct, and dashboard API helpers. All E2E test files in the package will use this.

```go
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

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
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
	Name        string
	Description string
	Response    string // JSON text returned by CallTool
}

// startMockBackend starts an in-process mcp-go HTTP server with the given tools
// and returns the URL (e.g., "http://127.0.0.1:12345/mcp") and a cleanup func.
func startMockBackend(t *testing.T, tools []toolDef) string {
	t.Helper()

	srv := mcpserver.NewMCPServer("mock-backend", "0.1.0")
	for _, td := range tools {
		td := td
		srv.AddTool(
			gomcp.Tool{
				Name:        td.Name,
				Description: td.Description,
				InputSchema: gomcp.ToolInputSchema{Type: "object"},
			},
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
	Servers     []testServerConfig `json:"servers"`
	Rules       []testRuleConfig   `json:"rules"`
	Port        int                `json:"port"`
	OpenBrowser bool               `json:"open_browser"`
	Audit       testAuditConfig    `json:"audit"`
	Log         testLogConfig      `json:"log"`
}

type testServerConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`
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
		Servers: []testServerConfig{
			{Name: "echo", Type: "http", URL: backendURL},
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
	if err := brokerCmd.Start(); err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() {
		_ = brokerCmd.Process.Signal(os.Interrupt)
		_ = brokerCmd.Wait()
	})

	brokerURL := fmt.Sprintf("http://127.0.0.1:%d", brokerPort)

	// Wait for broker to be ready (poll dashboard).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(brokerURL + "/dashboard/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Connect MCP client.
	mcpClient, err := client.NewStreamableHttpClient(brokerURL+"/mcp", transport.WithHTTPHeaders(map[string]string{}))
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
	resp, err := http.Get(s.BrokerURL + "/dashboard/api/pending")
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
	resp, err := http.Post(s.BrokerURL+"/dashboard/api/decide", "application/json", bytes.NewReader(body))
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
	resp, err := http.Get(s.BrokerURL + "/dashboard/api/tools")
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
	resp, err := http.Get(url)
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
```

**Step 2: Run `go vet` to check the file compiles**

Run (from `mcp-broker/`): `go vet -tags=e2e ./test/e2e/...`

Expected: no errors (no test functions yet, but the package should compile)

**Step 3: Commit**

```bash
cd mcp-broker && git add test/e2e/teststack_test.go
git commit -m "test(mcp-broker): add E2E TestStack helper and mock backend"
```

---

### Task 2: Write approve and deny E2E tests

**Files:**
- Create: `mcp-broker/test/e2e/e2e_test.go`

**Step 1: Write the approve and deny tests**

```go
//go:build e2e

package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var defaultTools = []toolDef{
	{Name: "say_hello", Description: "Says hello", Response: `{"message":"hello"}`},
}

func TestE2E_ApproveToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	// Call tool in goroutine — it blocks on approval.
	type callResult struct {
		text string
		err  error
	}
	ch := make(chan callResult, 1)
	go func() {
		result, err := s.callTool("echo.say_hello", map[string]any{})
		if err != nil {
			ch <- callResult{err: err}
			return
		}
		// Extract text from first content block.
		text := ""
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(map[string]any); ok {
				text, _ = tc["text"].(string)
			}
		}
		ch <- callResult{text: text}
	}()

	// Wait for pending request, then approve.
	pending := s.waitForPending(5 * time.Second)
	require.Len(t, pending, 1)
	require.Equal(t, "echo.say_hello", pending[0].Tool)
	s.approve(pending[0].ID)

	// Wait for tool call to complete.
	r := <-ch
	require.NoError(t, r.err)
	require.Contains(t, r.text, "hello")

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "echo.say_hello", audit.Records[0].Tool)
	require.Equal(t, "require-approval", audit.Records[0].Verdict)
	require.NotNil(t, audit.Records[0].Approved)
	require.True(t, *audit.Records[0].Approved)
}

func TestE2E_DenyToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{Tools: defaultTools})

	type callResult struct {
		isError bool
		err     error
	}
	ch := make(chan callResult, 1)
	go func() {
		result, err := s.callTool("echo.say_hello", map[string]any{})
		if err != nil {
			ch <- callResult{err: err}
			return
		}
		ch <- callResult{isError: result.IsError}
	}()

	pending := s.waitForPending(5 * time.Second)
	require.Len(t, pending, 1)
	s.deny(pending[0].ID)

	r := <-ch
	require.NoError(t, r.err)       // MCP call itself succeeds...
	require.True(t, r.isError)      // ...but the tool result is an error.

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.NotNil(t, audit.Records[0].Approved)
	require.False(t, *audit.Records[0].Approved)
}
```

Note: the `result.Content` field in mcp-go is `[]mcp.Content` which gets JSON-decoded as `[]any`. The exact shape depends on the mcp-go version. The test extracts text content and checks for the `"hello"` substring. If the content shape differs at runtime, adjust the extraction logic — the important thing is that the response contains the backend's canned result.

**Step 2: Run the E2E tests**

Run (from `mcp-broker/`): `go test -race -tags=e2e -timeout=60s -v ./test/e2e/...`

Expected: both tests PASS — approve returns the tool result, deny returns an error result. Debug broker logs go to stderr.

If the `result.Content` type assertion doesn't match, inspect the actual type with `t.Logf("%T %+v", result.Content, result.Content)` and fix the extraction. The mcp-go `CallToolResult.Content` is `[]Content` where `Content` has a `TextContent` variant with a `.Text` field. You may need to use a type switch on `result.Content[0]` — check the mcp-go `mcp.TextContent` type.

**Step 3: Commit**

```bash
cd mcp-broker && git add test/e2e/e2e_test.go
git commit -m "test(mcp-broker): add approve and deny E2E tests"
```

---

### Task 3: Write rules-based tests (allow and deny by policy)

**Files:**
- Modify: `mcp-broker/test/e2e/e2e_test.go`

**Step 1: Add the two rules-based tests**

Append to `e2e_test.go`:

```go
func TestE2E_AllowedToolCall(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "allow"}},
	})

	// Tool call should return immediately (no approval needed).
	result, err := s.callTool("echo.say_hello", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Verify audit log shows verdict=allow and no approval field.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "allow", audit.Records[0].Verdict)
	require.Nil(t, audit.Records[0].Approved)
}

func TestE2E_DeniedByRules(t *testing.T) {
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Rules: []testRuleConfig{{Tool: "echo.*", Verdict: "deny"}},
	})

	// Tool call should return an error immediately.
	result, err := s.callTool("echo.say_hello", map[string]any{})
	require.NoError(t, err)       // MCP call succeeds...
	require.True(t, result.IsError) // ...but tool result is an error.

	// Verify audit log.
	audit := s.getAudit("", 10, 0)
	require.Equal(t, 1, audit.Total)
	require.Equal(t, "deny", audit.Records[0].Verdict)
}
```

**Step 2: Run the E2E tests**

Run (from `mcp-broker/`): `go test -race -tags=e2e -timeout=60s -v -run 'TestE2E_Allowed|TestE2E_Denied' ./test/e2e/...`

Expected: both PASS.

**Step 3: Commit**

```bash
cd mcp-broker && git add test/e2e/e2e_test.go
git commit -m "test(mcp-broker): add rules-based E2E tests (allow, deny)"
```

---

### Task 4: Write dashboard tools listing test

**Files:**
- Modify: `mcp-broker/test/e2e/e2e_test.go`

**Step 1: Add the tools listing test**

Append to `e2e_test.go`:

```go
func TestE2E_DashboardToolsListing(t *testing.T) {
	tools := []toolDef{
		{Name: "greet", Description: "Greets the user", Response: `"hi"`},
		{Name: "farewell", Description: "Says goodbye", Response: `"bye"`},
		{Name: "status", Description: "Returns status", Response: `"ok"`},
	}
	s := newTestStack(t, stackOpts{
		Tools: tools,
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	resp := s.getTools()
	require.Len(t, resp.Tools, 3)

	// Tools should be sorted by name and prefixed with server name.
	names := make([]string, len(resp.Tools))
	for i, tool := range resp.Tools {
		names[i] = tool.Name
	}
	require.Contains(t, names, "echo.farewell")
	require.Contains(t, names, "echo.greet")
	require.Contains(t, names, "echo.status")

	// Verify descriptions are preserved.
	for _, tool := range resp.Tools {
		if tool.Name == "echo.greet" {
			require.Equal(t, "Greets the user", tool.Description)
		}
	}
}
```

**Step 2: Run the test**

Run (from `mcp-broker/`): `go test -race -tags=e2e -timeout=60s -v -run TestE2E_DashboardTools ./test/e2e/...`

Expected: PASS.

**Step 3: Commit**

```bash
cd mcp-broker && git add test/e2e/e2e_test.go
git commit -m "test(mcp-broker): add dashboard tools listing E2E test"
```

---

### Task 5: Write audit log pagination test

**Files:**
- Modify: `mcp-broker/test/e2e/e2e_test.go`

**Step 1: Add the audit pagination test**

Append to `e2e_test.go`:

```go
func TestE2E_AuditLogPagination(t *testing.T) {
	tools := []toolDef{
		{Name: "say_hello", Description: "Says hello", Response: `{"message":"hello"}`},
		{Name: "say_bye", Description: "Says bye", Response: `{"message":"bye"}`},
	}
	s := newTestStack(t, stackOpts{
		Tools: tools,
		Rules: []testRuleConfig{{Tool: "*", Verdict: "allow"}},
	})

	// Make 5 tool calls (3 say_hello, 2 say_bye).
	for i := 0; i < 3; i++ {
		_, err := s.callTool("echo.say_hello", map[string]any{})
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		_, err := s.callTool("echo.say_bye", map[string]any{})
		require.NoError(t, err)
	}

	// Verify total count.
	all := s.getAudit("", 50, 0)
	require.Equal(t, 5, all.Total)

	// Verify pagination: page 1.
	page1 := s.getAudit("", 2, 0)
	require.Len(t, page1.Records, 2)
	require.Equal(t, 5, page1.Total)

	// Verify pagination: page 2.
	page2 := s.getAudit("", 2, 2)
	require.Len(t, page2.Records, 2)

	// Verify pagination: page 3 (partial).
	page3 := s.getAudit("", 2, 4)
	require.Len(t, page3.Records, 1)

	// Verify filtering by tool name.
	filtered := s.getAudit("say_hello", 50, 0)
	require.Equal(t, 3, filtered.Total)
	for _, rec := range filtered.Records {
		require.Contains(t, rec.Tool, "say_hello")
	}
}
```

**Step 2: Run the test**

Run (from `mcp-broker/`): `go test -race -tags=e2e -timeout=60s -v -run TestE2E_AuditLog ./test/e2e/...`

Expected: PASS.

**Step 3: Commit**

```bash
cd mcp-broker && git add test/e2e/e2e_test.go
git commit -m "test(mcp-broker): add audit log pagination E2E test"
```

---

### Task 6: Add Makefile target and update documentation

**Files:**
- Modify: `mcp-broker/Makefile`
- Modify: `mcp-broker/CLAUDE.md`

**Step 1: Add `test-e2e` target to Makefile**

Add to the `.PHONY` line and add the target after `test-integration`:

In `mcp-broker/Makefile`, update the `.PHONY` line:
```makefile
.PHONY: build install test test-integration test-e2e lint fmt tidy audit
```

Add this target after the `test-integration` target:
```makefile
test-e2e:
	go test -race -tags=e2e -timeout=60s ./test/e2e/...
```

**Step 2: Update CLAUDE.md**

In `mcp-broker/CLAUDE.md`, add `make test-e2e` to the development commands block, after `test-integration`:
```
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
```

Also add a note after the existing "Integration tests use `//go:build integration`." line:
```
E2E tests use `//go:build e2e` and live in `test/e2e/`. They build and run the real binary as a subprocess.
```

**Step 3: Run the full E2E suite to verify**

Run (from `mcp-broker/`): `make test-e2e`

Expected: all 6 tests pass.

**Step 4: Commit**

```bash
cd mcp-broker && git add Makefile CLAUDE.md
git commit -m "chore(mcp-broker): add test-e2e make target and docs"
```
