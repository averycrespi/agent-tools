# MCP Broker E2E Test Design

## Goal

Add end-to-end tests that exercise the full MCP broker pipeline: agent connects via MCP, calls tools, approval flow works through the dashboard API, tools listing is correct, and audit log records everything properly.

## Architecture

Each test spins up an isolated stack as subprocesses:

```
Test Process
├── Mock MCP Backend (mcp-go HTTP server, in-process, random port)
├── MCP Broker (real binary started as subprocess with --config <tempdir>/config.json)
└── MCP Client (mcp-go client connecting to broker's /mcp endpoint)
```

### Key decisions

- **Subprocess broker** — `TestMain` compiles the binary once. Each test starts it with `--config` pointing at a temp config. This avoids duplicating wiring logic from `serve.go` and tests the real CLI.
- **In-process mock backend** — A minimal mcp-go HTTP server registers dummy tools and returns canned responses. Runs in the test process on a random port.
- **mcp-go client** — Tests connect to the broker using the real MCP client library for protocol fidelity.
- **HTTP API for dashboard interactions** — No browser needed. Tests use the existing REST endpoints (`/api/pending`, `/api/decide`, `/api/tools`, `/api/audit`).
- **Config isolation** — Each test writes its own `config.json` and `audit.db` to `t.TempDir()`. The existing `--config` flag handles this. `openBrowser: false` prevents browser popups.

## File Structure

```
mcp-broker/
  test/e2e/
    e2e_test.go          # //go:build e2e — all test functions
    teststack_test.go    # TestStack helper, mock backend, API helpers
```

## TestStack Helper

```go
type TestStack struct {
    BrokerURL string           // http://127.0.0.1:<port>
    Client    *client.Client   // mcp-go client connected to broker's /mcp
    t         *testing.T
}
```

### Lifecycle

1. **TestMain** — `go build -o <tmpdir>/mcp-broker ./cmd/mcp-broker`, store binary path in package var
2. **NewTestStack(t, opts):**
   a. Start mock MCP backend (mcp-go HTTP server on `:0`, registers tools from `opts.Tools`)
   b. Write temp `config.json` to `t.TempDir()`:
      ```json
      {
        "servers": [{"name": "echo", "type": "http", "url": "http://127.0.0.1:<backend-port>/mcp"}],
        "rules": [{"tool": "*", "verdict": "require-approval"}],
        "port": 0,
        "openBrowser": false,
        "audit": {"path": "<tempdir>/audit.db"}
      }
      ```
      Note: port 0 won't work with the current config (broker uses configured port, not `:0`). We'll pick a free port in the test by binding+closing a listener, then set it in the config.
   c. Start broker subprocess: `<binary> serve --config <tempdir>/config.json`
   d. Poll `GET /dashboard/` until ready (with timeout)
   e. Connect mcp-go client to `http://127.0.0.1:<port>/mcp`
   f. Register `t.Cleanup()` — send SIGTERM to broker, wait, close mock backend

### StackOpts

```go
type StackOpts struct {
    Tools []ToolDef     // tools the mock backend registers
    Rules []RuleConfig  // broker rules (default: require-approval for *)
}

type ToolDef struct {
    Name        string
    Description string
    InputSchema map[string]any
    Handler     func(args map[string]any) (any, error) // canned response
}
```

### Dashboard API Helpers

```go
func (s *TestStack) GetPending() ([]PendingRequest, error)        // GET /api/pending
func (s *TestStack) Approve(id string) error                      // POST /api/decide {id, decision: "approve"}
func (s *TestStack) Deny(id string) error                         // POST /api/decide {id, decision: "deny"}
func (s *TestStack) GetTools() ([]ToolInfo, error)                // GET /api/tools
func (s *TestStack) GetAudit(tool string, limit, offset int) (AuditResponse, error)  // GET /api/audit
func (s *TestStack) WaitForPending(timeout time.Duration) (PendingRequest, error)     // Poll /api/pending until non-empty
```

## Test Cases

### 1. TestE2E_ApproveToolCall

- Rules: `require-approval` for `*`
- Backend tool: `echo.say_hello` returns `{"message": "hello"}`
- Call tool in goroutine (blocks on approval)
- `WaitForPending()` — verify request appears
- `Approve(id)` — approve it
- Goroutine returns — verify result contains `{"message": "hello"}`
- `GetAudit()` — verify record with `approved=true`, verdict `require-approval`

### 2. TestE2E_DenyToolCall

- Same setup as above
- `Deny(id)` instead of approve
- Verify client receives error result
- `GetAudit()` — verify record with `approved=false`

### 3. TestE2E_AllowedToolCall

- Rules: `allow` for `echo.*`
- Call tool — returns immediately (no approval needed)
- Verify result is correct
- `GetAudit()` — verify verdict `allow`, no approval field

### 4. TestE2E_DeniedByRules

- Rules: `deny` for `echo.*`
- Call tool — returns error immediately
- `GetAudit()` — verify verdict `deny`

### 5. TestE2E_DashboardToolsListing

- Backend registers 3 tools with distinct names and descriptions
- `GetTools()` — verify all 3 appear with correct names, descriptions, and server prefix

### 6. TestE2E_AuditLogPagination

- Rules: `allow` for `*` (so calls don't block)
- Make 5 tool calls
- `GetAudit(limit=2, offset=0)` — verify 2 records, total=5
- `GetAudit(limit=2, offset=2)` — verify next 2 records
- `GetAudit(tool="say_hello", ...)` — verify filtering works

## Changes Required

### New files
- `mcp-broker/test/e2e/e2e_test.go` — test functions
- `mcp-broker/test/e2e/teststack_test.go` — TestStack, mock backend, helpers

### Modified files
- `mcp-broker/Makefile` — add `test-e2e` target:
  ```makefile
  test-e2e:
  	go test -race -tags=e2e -timeout=60s ./test/e2e/...
  ```
- `mcp-broker/CLAUDE.md` — document `make test-e2e`

### No changes needed
- `--config` flag already exists on rootCmd
- Dashboard REST API endpoints already exist (`/api/pending`, `/api/decide`, `/api/tools`, `/api/audit`)
