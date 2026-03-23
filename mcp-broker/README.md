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

## Security

mcp-broker is designed for **local use only** — it listens on localhost and must never be exposed to the public internet. The authentication layer is a lightweight guard against unauthorized local processes (rogue scripts, browser tabs, compromised extensions) accessing your tools and credentials. It is not a substitute for network-level security.

**Threat model:** Prevent other processes on your machine from calling the broker's HTTP endpoints without authorization. This covers casual/accidental access and opportunistic localhost attacks, not a determined attacker with root access to your machine.

**What auth provides:**
- A random bearer token required on every request (MCP and dashboard)
- Cookie-based session for the browser dashboard
- Constant-time token comparison to prevent timing attacks

**What auth does NOT provide:**
- Protection against an attacker who can read your filesystem (they can read the token file)
- TLS/encryption (traffic is plain HTTP on localhost)
- User accounts or role-based access — there is one token for everything
- Automatic token rotation (use `mcp-broker token regen` to rotate manually)

## Quick start

```bash
# Build
make build

# Run (creates default config on first run)
./mcp-broker serve

# Dashboard URL (with auth token) is printed to stderr on startup
# MCP endpoint at http://localhost:8200/mcp (requires Bearer token)
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
      "name": "github-remote",
      "type": "sse",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {"Authorization": "Bearer $GITHUB_TOKEN"}
    },
    {
      "name": "internal",
      "type": "http",
      "url": "http://localhost:3000/mcp"
    },
    {
      "name": "atlassian",
      "type": "http",
      "url": "https://mcp.atlassian.com",
      "oauth": true
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
| `type` | Transport type: omit for stdio, `"http"` for Streamable HTTP, `"sse"` for SSE |
| `url` | URL for HTTP transport |
| `headers` | HTTP headers; `$VAR` and `${VAR}` references are expanded from the process environment |
| `oauth` | OAuth config: `true` for defaults (dynamic registration, PKCE) or `{"client_id": "...", "scopes": [...]}` for overrides |

### OAuth

Servers that require OAuth can use `"oauth": true` for zero-config setup (dynamic client registration, PKCE, server-default scopes):

```json
{"name": "atlassian", "type": "http", "url": "https://mcp.atlassian.com", "oauth": true}
```

Or provide overrides when needed:

```json
{
  "name": "custom",
  "type": "http",
  "url": "https://mcp.example.com",
  "oauth": {
    "client_id": "my-app",
    "scopes": ["read", "write"],
    "auth_server_url": "https://auth.example.com"
  }
}
```

On first connect, mcp-broker opens your browser to authenticate. Tokens are stored in the OS keychain (macOS Keychain / Linux Secret Service) and refreshed automatically.

### Rules

Rules are evaluated top-to-bottom, first match wins. Patterns use Go's `filepath.Match` glob syntax.

| Verdict | Behavior |
|---------|----------|
| `allow` | Tool call proceeds immediately |
| `deny` | Tool call is rejected |
| `require-approval` | Tool call blocks until approved via dashboard |

Default (no matching rule): `require-approval`.

## Authentication

On first run, mcp-broker generates a random auth token and saves it to `~/.config/mcp-broker/auth-token`. All endpoints require this token.

**MCP clients** pass the token as an HTTP header:

```json
{
  "mcpServers": {
    "broker": {
      "type": "streamableHttp",
      "url": "http://localhost:8200/mcp",
      "headers": {
        "Authorization": "Bearer <token>"
      }
    }
  }
}
```

**Dashboard** opens automatically in your browser with the token. A cookie is set on first visit so you don't need to re-authenticate. If you need the URL again, it's printed to stderr every time the broker starts.

**Token rotation:**

```bash
mcp-broker token regen    # Generate a new token (invalidates all existing sessions)
```

## CLI

```
mcp-broker serve              # Start the broker
mcp-broker serve --log-level debug
mcp-broker token regen        # Regenerate auth token
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
  server/               Backend MCP client (stdio, HTTP, SSE, OAuth transports)
  dashboard/            Web UI with approval flow, SSE, audit viewer
  broker/               Core orchestrator (rules → approval → proxy → audit)
```
