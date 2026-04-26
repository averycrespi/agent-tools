# mcp-broker Design

## Motivation

Autonomous AI agents need access to external systems (GitHub, Jira, Slack, cloud APIs). The standard approach is to run MCP servers alongside the agent, but this means the agent's environment holds API tokens, has network access, and can reach any endpoint those tokens authorize. For sandboxed agents — the kind you actually want for autonomous work — this is a problem: either the agent stays in the sandbox and can't reach external tools, or you punch holes in the sandbox and lose the security guarantees.

Prior approaches all hit walls:

- **Allow-listing tool calls** — doesn't scale. A real workflow needs 90+ permissions, and every new tool triggers another prompt.
- **Sandbox classifiers** — can't reliably distinguish safe from unsafe operations. Agents still get blocked.
- **Host-guest VMs** — keeps the agent isolated, but syncing two environments is painful operational overhead.
- **Giving agents credentials directly** — defeats the purpose of sandboxing. If the agent holds tokens, compromise means full access.

The core insight: **decouple agent autonomy from system privilege**. The agent doesn't need credentials or network access — it just needs to make MCP tool calls. A trusted broker on the host can hold the secrets, connect to the real MCP servers, and proxy calls through a policy layer.

mcp-broker is that broker:

- **Secrets stay on the host** — the agent connects to mcp-broker as its only MCP server. mcp-broker runs outside the sandbox, holds API tokens, and spawns/connects to backend MCP servers. The agent never sees credentials.
- **Policy controls access** — glob-based rules determine which tools are allowed, denied, or require human approval. Default is require-approval (fail-closed).
- **Human in the loop** — sensitive operations appear in a web dashboard where a human can approve or deny them before they execute.
- **Full audit trail** — every tool call is logged with arguments, verdict, approval status, and result.

## Architecture

```
                          ┌─────────────────────────────────────────────┐
                          │                 mcp-broker                  │
                          │                                             │
Agent ──MCP(/mcp)──▶      │  ┌─────────┐   ┌──────────┐   ┌────────┐  │
                          │  │  Rules   │──▶│ Approval │──▶│ Proxy  │  │──MCP──▶ Backend A
                          │  │ Engine   │   │(Dashboard│   │(Manager│  │──MCP──▶ Backend B
                          │  └─────────┘   └──────────┘   └────────┘  │──MCP──▶ Backend C
                          │       │              │             │       │
                          │       └──────────────┼─────────────┘       │
                          │                      ▼                     │
                          │               ┌────────────┐               │
                          │               │   Audit    │               │
                          │               │  (SQLite)  │               │
                          │               └────────────┘               │
                          │                                             │
Human ──HTTP(:8200)──▶    │            Dashboard (Web UI)               │
                          └─────────────────────────────────────────────┘
```

### Single binary, single port

mcp-broker is a single Go binary serving on a single port (default 8200):

- `/mcp` — Streamable HTTP MCP endpoint for agents
- `/` — Web dashboard for humans (approval, tools, audit log)

### Pipeline

Every tool call flows through the same pipeline:

1. **Rules engine** — Evaluates the tool name against an ordered list of glob rules. Each rule maps a pattern to a verdict: `allow`, `deny`, or `require-approval`. First match wins; default is `require-approval`.

2. **Approval** — If the verdict is `require-approval`, the call blocks and appears in the web dashboard. A human approves or denies it. If no approver is configured, the call is rejected.

3. **Proxy** — The call is forwarded to the backend MCP server that owns the tool. The broker strips the namespace prefix before forwarding.

4. **Audit** — Every call is recorded in a SQLite database with: timestamp, tool name, arguments, verdict, approval status, result, and any error.

### Tool namespacing

Each backend server has a name (from config). When tools are discovered, they are prefixed with `<server-name>.` to avoid collisions. For example, a server named `github` with a tool `search` becomes `github.search`.

## Components

### Config (`internal/config`)

Single JSON file at `~/.config/mcp-broker/config.json`. On first run, a default config is written. The `Refresh` function loads, overlays defaults for new fields, and writes back — useful for upgrading config after new features are added.

Config is loaded once at startup. Defaults:

- Host: `127.0.0.1` (must resolve to a loopback interface — validated at startup)
- Port: 8200
- Rules: `[{"tool": "*", "verdict": "require-approval"}]`
- Audit path: `~/.local/share/mcp-broker/audit.db`
- Log level: `info`

### Rules engine (`internal/rules`)

Stateless evaluator. Takes a list of `RuleConfig` (tool glob + verdict string) at construction time. `Evaluate(tool)` walks rules in order, returns the first matching verdict. Uses `filepath.Match` for glob matching, which supports `*` (single segment) and `?` wildcards.

### Audit (`internal/audit`)

SQLite database using `ncruces/go-sqlite3` (WASM-based, no CGO). WAL mode for concurrent read/write. Thread-safe via mutex. Records are inserted via prepared statement for performance.

The `Query` method supports:

- Tool name filtering (substring match via SQL LIKE)
- Pagination (limit/offset)
- Total count for pagination UI

### Server manager (`internal/server`)

Manages connections to backend MCP servers. At startup:

1. Connects to each configured server (stdio subprocess, HTTP, SSE, or OAuth)
2. Sends MCP `initialize` handshake
3. Calls `tools/list` to discover available tools
4. Builds a registry of `<server>.<tool>` → backend mapping

Tool descriptors are passed through to clients with full fidelity: in addition to name and input schema, the broker preserves each tool's `outputSchema`, `annotations` (including `title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`), and `_meta` from the upstream backend. The only field the broker rewrites is the tool name, which is prefixed with `<server>.` for routing.

The `Backend` interface abstracts transport:

- `stdioBackend` — spawns a subprocess, communicates via stdin/stdout
- `httpBackend` — connects via Streamable HTTP
- `sseBackend` — connects via Server-Sent Events
- `oauthBackend` — auto-detected on 401 responses from HTTP/SSE backends; tokens stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name). OAuth callback port is deterministic per server name (FNV hash → ephemeral port range).

HTTP/SSE backends use a plain client first and auto-upgrade to OAuth on 401 — they do not proactively trigger OAuth flows.

Failed backends are logged and skipped rather than failing the entire startup.

Environment variables in server config support `$VAR` expansion from the process environment, allowing secrets to be passed without hardcoding.

### Auth (`internal/auth`)

Bearer token authentication for the `/mcp` endpoint. Generates a 32-byte random token (hex-encoded, 64 chars) stored with `0600` file permissions (parent directories `0750`). The HTTP middleware validates tokens using `crypto/subtle.ConstantTimeCompare`. Token is generated on first `serve` if it doesn't already exist.

### Telegram approver (`internal/telegram`)

Optional Telegram Bot API-based approver. Uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed. When an approval is required, a message is sent to the configured chat; responses are correlated by Telegram `message_id`. Bot token and chat ID support `$VAR` expansion via `os.ExpandEnv` at startup.

### Dashboard (`internal/dashboard`)

Embedded single-page web application serving:

- **Approvals tab** — pending requests with approve/deny buttons, decided history
- **Tools tab** — discovered tools grouped by server; click a tool to see its input schema
- **Rules tab** — configured rules with the discovered tools matching each (read-only; for debugging verdicts)
- **Audit tab** — paginated audit log with tool filter

Real-time updates via Server-Sent Events (SSE). The dashboard also implements the `Approver` interface — the `Review` method blocks until a human makes a decision via the `/api/decide` endpoint.

### Broker (`internal/broker`)

The orchestrator. Wires together rules, approval, proxy, and audit. The `Handle` method is the single entry point for all tool calls. Interfaces:

- `ServerManager` — tool listing and call proxying
- `AuditLogger` — recording and querying audit entries
- `Approver` — human approval decisions

`MultiApprover` fans approval requests to all configured approvers (e.g., dashboard + Telegram) concurrently with a shared timeout. First response wins.

### CLI (`cmd/mcp-broker`)

Cobra-based CLI with commands:

- `serve` — starts the broker (loads config, connects backends, serves HTTP)
- `config path` — prints config file location
- `config refresh` — backfills new defaults
- `config edit` — opens config in `$EDITOR`

## Tech stack

| Component    | Library                                                                    |
| ------------ | -------------------------------------------------------------------------- |
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go)                              |
| CLI          | [cobra](https://github.com/spf13/cobra)                                    |
| SQLite       | [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) (WASM, no CGO) |
| Logging      | `log/slog` (stdlib)                                                        |
| Testing      | [testify](https://github.com/stretchr/testify)                             |

## Design decisions

**Single port for MCP + dashboard.** Simplifies deployment and configuration. The agent connects to `/mcp`, humans browse `/`.

**Glob-based rules, not regex.** Globs are simpler to read and write for the common case of matching tool name prefixes. `filepath.Match` is stdlib and well-understood.

**SQLite for audit, not a log file.** Enables querying, pagination, and filtering in the dashboard without external tools. WAL mode handles concurrent reads from the dashboard while the broker writes.

**Bearer token auth for agents, cookie auth for dashboard.** The `/mcp` endpoint requires a bearer token (32 random bytes, hex-encoded, stored with `0600` permissions). The dashboard uses a session cookie (`mcp-broker-auth`, `HttpOnly`, `SameSite=Strict`) so browsers don't need the raw token.

**Failed backends don't block startup.** If one of several backend servers is unavailable, the broker starts with the remaining servers rather than failing entirely. The failed backend is logged.

**Default verdict is require-approval.** Fail-closed by default — any tool not explicitly allowed requires human approval.

**Loopback-only listener, enforced at startup.** `server.ValidateLoopbackAddr` rejects any bind host that isn't a loopback IP or `localhost`. The bearer token protects against unauthorized local processes; the network boundary protects against everything else. Making network-reachability a hard error instead of a doc-only intent removes the "oops, I configured `0.0.0.0`" failure mode. Sandboxed agents reach the broker via Lima's user-mode networking, which forwards `host.lima.internal:8200` from the guest to the host's loopback — no non-loopback bind required.
