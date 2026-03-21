# MCP Broker

An MCP proxy that lets sandboxed agents use external tools without holding secrets.

Agents run in sandboxes with no credentials and restricted network access — but they still need to call GitHub, Jira, Slack, and other external APIs. mcp-broker runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint. Policy rules control which tools are allowed, sensitive operations require human approval via a web dashboard, and every call is audit-logged.

## How it works

```
Agent ──MCP──▶ mcp-broker ──MCP──▶ Backend servers
                  │
                  ├─ Rules engine (glob-based allow/deny/require-approval)
                  ├─ Human approval via web dashboard
                  └─ SQLite audit log
```

An agent connects to mcp-broker as a single MCP server. mcp-broker connects to one or more backend MCP servers (via stdio or HTTP), discovers their tools, and re-exposes them with `<server>.<tool>` namespacing. Every tool call flows through the pipeline:

1. **Rules check** — glob patterns match tool names to verdicts (`allow`, `deny`, `require-approval`)
2. **Approval** — if the verdict is `require-approval`, the call blocks until a human approves or denies it via the web dashboard
3. **Proxy** — the call is forwarded to the backend server
4. **Audit** — the call, verdict, and result are recorded in SQLite

## Quick start

```bash
# Build
make build

# Run (creates default config on first run)
./mcp-broker serve

# Dashboard at http://localhost:8200
# MCP endpoint at http://localhost:8200/mcp
```

## Configuration

Config lives at `~/.config/mcp-broker/config.json` (or `$XDG_CONFIG_HOME/mcp-broker/config.json`).

```json
{
  "servers": [
    {
      "name": "github",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {"GITHUB_TOKEN": "$GITHUB_TOKEN"}
    },
    {
      "name": "remote",
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {"Authorization": "Bearer $GITHUB_TOKEN"}
    }
  ],
  "rules": [
    {"tool": "github.search_*", "verdict": "allow"},
    {"tool": "github.push*", "verdict": "require-approval"},
    {"tool": "*", "verdict": "require-approval"}
  ],
  "port": 8200,
  "audit": {
    "path": "~/.local/share/mcp-broker/audit.db"
  },
  "log": {
    "level": "info"
  }
}
```

### Servers

Each server entry connects to a backend MCP server:

| Field | Description |
|-------|-------------|
| `name` | Unique name; used as tool prefix (e.g. `github.search`) |
| `command` | Command to spawn (stdio transport, default) |
| `args` | Command arguments |
| `env` | Environment variables; `$VAR` and `${VAR}` references are expanded from the process environment |
| `type` | Transport type: omit for stdio, `"http"` for Streamable HTTP |
| `url` | URL for HTTP transport |
| `headers` | HTTP headers; `$VAR` and `${VAR}` references are expanded from the process environment |

### Rules

Rules are evaluated top-to-bottom, first match wins. Patterns use Go's `filepath.Match` glob syntax.

| Verdict | Behavior |
|---------|----------|
| `allow` | Tool call proceeds immediately |
| `deny` | Tool call is rejected |
| `require-approval` | Tool call blocks until approved via dashboard |

Default (no matching rule): `require-approval`.

## CLI

```
mcp-broker serve              # Start the broker
mcp-broker serve --log-level debug
mcp-broker config path        # Print config file path
mcp-broker config refresh     # Backfill new defaults into config
mcp-broker config edit        # Open config in $EDITOR
```

## Development

```bash
make build              # Build binary to ./mcp-broker
make test               # Run tests with race detector
make test-integration   # Run integration tests (-tags=integration)
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Requires Go 1.25+. Tool dependencies (golangci-lint, goimports, govulncheck) are managed via `go tool` directives in `go.mod`.

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.

```
cmd/mcp-broker/         CLI entry point (Cobra)
internal/
  config/               JSON config load/save/refresh
  rules/                Glob-based rule engine
  audit/                SQLite audit logger
  server/               Backend MCP client (stdio + HTTP transports)
  dashboard/            Web UI with approval flow, SSE, audit viewer
  broker/               Core orchestrator (rules → approval → proxy → audit)
```
