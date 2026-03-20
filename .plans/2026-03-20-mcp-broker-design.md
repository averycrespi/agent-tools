# mcp-broker Design

## Overview

mcp-broker is a generic MCP proxy that connects to multiple backend MCP servers, discovers their tools, and exposes them through a single frontend MCP endpoint — with policy rules, human approval, and audit logging in between.

It is an adaptation and simplification of [Brocade](https://github.com/averycrespi/brocade). Where Brocade requires writing custom Go provider code for each backend service, mcp-broker replaces that with zero-code configuration: adding a backend is a JSON config entry, not a Go package.

## Core Flow

```
Agent (Claude Code, etc.)
  │
  │ MCP (Streamable HTTP)
  ▼
┌─────────────────────────┐
│       mcp-broker        │
│                         │
│  ┌───────────────────┐  │
│  │   Rules Engine    │  │  ← glob-based allow/deny/require-approval
│  └────────┬──────────┘  │
│  ┌────────▼──────────┐  │
│  │   Approval Gate   │  │  ← web dashboard for human review
│  └────────┬──────────┘  │
│  ┌────────▼──────────┐  │
│  │   MCP Proxy       │──┼──► Backend MCP Server (stdio or HTTP)
│  └────────┬──────────┘  │
│  ┌────────▼──────────┐  │
│  │   Audit Log       │  │  ← SQLite
│  └───────────────────┘  │
└─────────────────────────┘
```

### Request Pipeline

```
tool call received → rules check → approval gate (if needed) → proxy to backend → audit → respond
```

1. **Rules check**: Glob-match the tool name against configured rules. First match wins. Verdict is `allow`, `deny`, or `require-approval`.
2. **Approval gate**: If verdict is `require-approval`, the request is held until a human approves or denies it via the web dashboard.
3. **Proxy**: Forward the tool call to the appropriate backend MCP server and return the result.
4. **Audit**: Log the full request/response cycle to SQLite.

## Configuration

Single file at `~/.config/mcp-broker/config.json`:

```json
{
  "servers": [
    {
      "name": "github",
      "command": "gh",
      "args": ["mcp-server"],
      "env": { "GH_TOKEN": "$GH_TOKEN" }
    },
    {
      "name": "linear",
      "type": "http",
      "url": "http://localhost:3000/mcp"
    },
    {
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
    }
  ],
  "rules": [
    { "tool": "github.*", "verdict": "allow" },
    { "tool": "filesystem.write_file", "verdict": "require-approval" },
    { "tool": "*", "verdict": "deny" }
  ],
  "port": 8200,
  "audit": {
    "path": "~/.config/mcp-broker/audit.db"
  },
  "log": {
    "level": "info"
  }
}
```

### Server Config

Each entry in the `servers` array defines a backend MCP server:

- **stdio servers**: Set `command` and `args`. mcp-broker spawns the process and communicates via stdin/stdout. Optional `env` map supports `$VAR` expansion from the process environment.
- **HTTP servers**: Set `type: "http"` and `url`. mcp-broker connects as an MCP client over Streamable HTTP.
- **name** (required): Used as the tool name prefix and for display in the dashboard.

### Rules

First-match glob rules using `filepath.Match` patterns. Three verdicts:

- `allow` — execute immediately
- `deny` — reject with error
- `require-approval` — hold for human decision via dashboard

If no rule matches, the default verdict is `require-approval`.

### Tool Namespacing

Tools from backend servers are auto-prefixed with the server name to avoid collisions:

```
Backend "github" exposes: search, get_pr
Backend "linear" exposes: search, get_issue

Exposed as:
  github.search
  github.get_pr
  linear.search
  linear.get_issue
```

Rules use the prefixed names, so `github.*` matches all tools from the github server.

## Terminology

Simplified from Brocade's OS kernel metaphor:

| Brocade         | mcp-broker    |
|-----------------|---------------|
| Provider        | Server        |
| Capability      | Tool          |
| Gatekeeper      | Rules         |
| Approver        | Approval      |
| Transporter     | (removed)     |
| Kernel          | Broker        |

## Go Module

```
agent-tools/              # repo root
├── go.work               # Go workspace: references mcp-broker/
├── .gitignore
├── LICENSE
├── README.md
└── mcp-broker/
    ├── go.mod             # module github.com/averycrespi/agent-tools/mcp-broker
    ├── ...
```

## Directory Structure

```
mcp-broker/
├── go.mod
├── .golangci.yml
├── Makefile
├── cmd/mcp-broker/
│   ├── main.go            # Entry point: cmd.Execute()
│   └── serve.go           # serve command implementation
├── internal/
│   ├── config/
│   │   └── config.go        # Load/save/defaults for config.json
│   ├── server/
│   │   └── server.go        # Backend MCP client management
│   │                        #   - spawn stdio / connect HTTP
│   │                        #   - discover tools, auto-prefix names
│   │                        #   - proxy tool calls to backends
│   ├── rules/
│   │   └── rules.go         # Glob-based rule matching
│   ├── audit/
│   │   └── audit.go         # SQLite audit log (record + query)
│   ├── dashboard/
│   │   ├── dashboard.go     # HTTP server: approval UI + audit viewer + approval logic
│   │   └── index.html       # Embedded HTML (go:embed)
│   └── broker/
│       └── broker.go        # Core — wires everything together
│                             #   - frontend MCP server
│                             #   - routes tool calls through pipeline
│                             #   - manages lifecycle
```

### Package Responsibilities

**`broker/`** — The core orchestrator. Creates the frontend MCP server, registers discovered tools, and routes every tool call through the pipeline (rules → approval → proxy → audit). Manages startup and graceful shutdown.

**`server/`** — Manages backend MCP server connections. Spawns stdio processes or connects to HTTP endpoints. Calls `tools/list` to discover tools on startup. Proxies `tools/call` requests to the correct backend. Handles tool name prefixing.

**`rules/`** — Evaluates tool names against glob-based rules. Returns a verdict (allow/deny/require-approval). First match wins, default is require-approval.

**`audit/`** — SQLite-backed audit log. Records every tool call with timestamp, tool name, arguments, rule verdict, approval decision, result, and error. Supports filtered/paginated queries for the dashboard.

**`dashboard/`** — Serves the web UI, its API endpoints, and manages the approval flow (pending request queue, decision channels). Embedded HTML via `go:embed`. Three tabs: pending approvals, tool catalog, audit log. Real-time updates via SSE.

**`config/`** — Loads, saves, and validates config.json. Writes defaults on first run. Supports refresh (backfill new fields without overwriting existing).

### Dependency Injection

Services accept interfaces for testability. The broker accepts interfaces for rules, audit, dashboard, and server management — not concrete types.

```go
type Broker struct {
    servers  ServerManager
    rules    RulesEvaluator
    audit    AuditLogger
    dashboard Dashboard
    logger   *slog.Logger
}
```

Backend MCP connections are behind a `ServerManager` interface so tests can mock tool discovery and proxying without spawning real processes.

### Logging

Uses `log/slog` from the standard library. Log level is driven by config (`"log": {"level": "info"}`). The `serve` command accepts a `--log-level` flag to override.

### Error Handling

- All Cobra commands use `RunE` (not `Run`) to return errors.
- Root command sets `SilenceUsage: true` and `SilenceErrors: true` — runtime errors don't dump usage text.
- `main.go` handles the top-level error: log it with `slog.Error` and `os.Exit(1)`.
- Wrap errors with context using `%w`: `fmt.Errorf("failed to connect to server %q: %w", name, err)`.

### Testing

- Unit tests use mock implementations of interfaces (`testify/mock`).
- Test naming: `TestType_Method_Scenario`.
- Always run with race detection: `go test -race ./...`.

## CLI

```
mcp-broker serve          # Start the broker
mcp-broker config path    # Print config file path
mcp-broker config refresh # Backfill new defaults without overwriting existing
mcp-broker config edit    # Open config in $EDITOR
```

### `serve` Behavior

1. Load config (create default if missing)
2. Connect to all backend servers (stdio spawn or HTTP connect)
3. Discover tools from each backend via `tools/list`
4. Start HTTP server on configured port (MCP at `/mcp`, dashboard at `/`)
5. Log: `"Discovered 12 tools from 3 servers"`
6. Graceful shutdown on SIGINT

### Agent Connection

Claude Code config to connect to the broker:

```json
{
  "mcpServers": {
    "broker": {
      "type": "http",
      "url": "http://localhost:8200/mcp"
    }
  }
}
```

## Dashboard

Adapted from Brocade's embedded web UI. Three tabs:

### Pending Approvals

Real-time list of tool calls waiting for human decision. Each shows the tool name, arguments, and approve/deny buttons. Uses SSE for live updates.

### Tools

Auto-discovered catalog grouped by server. Shows every tool with its name, description, and input schema. Purely informational — tools are discovered automatically from backends.

### Audit Log

Filterable, paginated view of all tool calls. Each record shows:
- Timestamp
- Tool name
- Arguments
- Rule verdict
- Approval decision (if applicable)
- Result or error

### API Endpoints

| Endpoint          | Method | Description                     |
|-------------------|--------|---------------------------------|
| `/events`         | GET    | SSE stream for real-time updates |
| `/api/decide`     | POST   | Approve/deny a pending request  |
| `/api/pending`    | GET    | List pending approvals          |
| `/api/tools`      | GET    | List all discovered tools       |
| `/api/audit`      | GET    | Query audit log                 |

## What We're NOT Building

- No keychain/credential management — use env vars
- No OAuth — backends handle their own auth
- No HTTP /rpc transport — MCP only
- No JSONFile auditor — SQLite only
- No OPA or dynamic rule engines — static glob rules only
- No agent identity tracking — simplified away
- No `auth` CLI command

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `github.com/mark3labs/mcp-go` | MCP protocol library (frontend server + backend clients) |
| `github.com/spf13/cobra` | CLI framework |
| `github.com/ncruces/go-sqlite3` | SQLite driver for audit log (pure Go, no CGO) |
| `golang.org/x/sync/errgroup` | Concurrent server management |
| `github.com/stretchr/testify` | Testing assertions and mocks |

### Go Module Tools

Installed via `tool` directive in `go.mod`, run via `go tool <name>`:

| Tool | Purpose |
|------|---------|
| `github.com/golangci/golangci-lint/cmd/golangci-lint` | Linting |
| `golang.org/x/tools/cmd/goimports` | Formatting and import organization |
| `golang.org/x/vuln/cmd/govulncheck` | Dependency vulnerability scanning |

### Linter Configuration

`.golangci.yml` (v2 format) at the `mcp-broker/` root:

- `default: standard` enables the standard linter set (govet, staticcheck, errcheck, unused, ineffassign).
- Additional linters: `errorlint`, `gocritic`, `gosec`.
- Formatters: `goimports`.
- Suppress `gosec` in test files.

### Makefile

| Target | Command |
|--------|---------|
| `build` | `go build -o /tmp/bin/mcp-broker ./cmd/mcp-broker` |
| `test` | `go test -race ./...` |
| `lint` | `go tool golangci-lint run` |
| `fmt` | `go tool goimports -w .` |
| `tidy` | `go mod tidy && go mod verify` |
| `audit` | `tidy` + `fmt` + `lint` + `test` + `go tool govulncheck ./...` |

`make audit` is the local equivalent of CI — run it before pushing.
