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
- **Full audit trail** — every tool call is logged with arguments, verdict, approval status, denial reason, and errors. Successful tool results are not stored, keeping returned data out of the audit database.

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

- `/mcp` — Streamable HTTP MCP endpoint for agents, protected by a configurable request body limit (`max_request_body_bytes`, default 10 MiB)
- `/` — Web dashboard for humans (approval, tools, audit log)

### Pipeline

Every tool call flows through the same pipeline:

1. **Rules engine** — Evaluates the tool name against an ordered list of glob rules. Each rule maps a pattern to a verdict: `allow`, `deny`, or `require-approval`. First match wins; default is `require-approval`. Deny rules may include an optional human-authored `reason`, returned to agents as `denied by rule: <reason>`.

2. **Approval** — If the verdict is `require-approval`, the call blocks and appears in the web dashboard. A human approves or denies it. Dashboard denials can include an optional reason, returned as `denied by user: <reason>`; binary denials return `denied by user`. Approval timeouts return `denied by timeout`. If no approver is configured, the call is rejected. Per-request `Mcp-Broker-Approval-Mode: reject` skips approval fan-out and immediately rejects calls whose verdict is `require-approval`; `allow` and `deny` verdicts are unchanged.

3. **Proxy** — The call is forwarded to the backend MCP server that owns the tool. The broker strips the namespace prefix before forwarding.

4. **Audit** — Every call is recorded in a SQLite database with: timestamp, tool name, arguments, verdict, approval status, denial reason, and any error. Successful tool results are deliberately not stored.

### Tool namespacing

Each backend server has a name (from config). When tools are discovered, they are prefixed with `<server-name>.` to avoid collisions. For example, a server named `github` with a tool `search` becomes `github.search`.

## Components

### Config (`internal/config`)

Single JSON file at `~/.config/mcp-broker/config.json`. On first run, a default config is written. The `Refresh` function loads, overlays defaults for new fields, and writes back — useful for upgrading config after new features are added.

Config is loaded once at startup. In addition to backend server and rule policy, config may include `tool_patches`: ordered load-time transforms for discovered tools. Tool patch patterns match broker-prefixed tool names with `filepath.Match`; the first matching patch applies. `disabled: true` removes the tool from the broker registry, and `annotations` merges field-by-field into MCP tool annotations (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`). `_meta` is passed through unchanged and is not patched.

Defaults:

- Host: `127.0.0.1` (must resolve to a loopback interface — validated at startup)
- Port: 8200
- Rules: `[{"tool": "*", "verdict": "require-approval"}]`
- Audit path: `~/.local/share/mcp-broker/audit.db`
- Log level: `info`

### Rules engine (`internal/rules`)

Stateless evaluator. Takes a list of `RuleConfig` at construction time. `Evaluate(tool, args)` walks rules in order and returns the first matching verdict. Uses `filepath.Match` for glob matching, which supports `*` (single segment) and `?` wildcards.

**Default verdict, fail-closed, first-match-wins.** Any tool not fully matched by a rule falls through to `RequireApproval`. This is unchanged by argument matching.

#### Argument matching

Each `RuleConfig` has optional `reason` and `args` fields. `reason` is primarily for deny rules; when present on a matching deny rule, the broker records `rule: <reason>` in audit and returns `denied by rule: <reason>` to the agent.

`args` is a list of argument patterns. When `args` is absent or empty, the rule matches on tool name alone (fully backward compatible). When `args` is non-empty, all patterns must match (AND semantics): a rule fires only if the tool name matches AND every pattern resolves and matches.

```json
{
  "tool": "local-git.push",
  "verdict": "allow",
  "args": [
    { "path": "remote", "match": "origin" },
    { "path": "commit.message", "match": { "regex": "^feat:" } }
  ]
}
```

This rule allows `push` only when `remote` is exactly `"origin"` and `commit.message` starts with `"feat:"`. Any other `push` call fails to match this rule and falls through to the next.

**Path syntax.** `path` is a dot-separated sequence of segments. Each segment is either a string key (object navigation) or a decimal integer (array index). Examples: `remote`, `commit.message`, `command.0`. No wildcards in v1. Empty segments (`a..b`) and the empty path (`""`) are rejected at engine construction.

**Resolution.** Each segment is applied in turn to the current node:

| Current node     | Segment kind | Behavior                     |
| ---------------- | ------------ | ---------------------------- |
| `map[string]any` | key          | descend; missing key → fail  |
| `[]any`          | index        | descend; out-of-range → fail |
| any other type   | any          | fail (type mismatch)         |

If resolution fails for any reason, the pattern fails, the rule fails to match, and evaluation continues to the next rule. This is fail-closed: an argument the rule can't inspect is treated as a non-match, not a pass.

**Value stringification.** The resolved value is converted to a string before matching using `encoding/json.Marshal`. Plain strings are unquoted; other types marshal to their JSON representation: `42` → `"42"`, `true` → `"true"`, `null` → `"null"`. To match into a container (object or array), use a deeper path to reach a scalar; matching against a marshaled object literal is allowed but rarely useful.

**Matchers.** Two kinds:

- **Exact:** bare JSON string in `match`. The resolved value must equal that string exactly.
- **Regex:** `{ "regex": "<RE2 pattern>" }` in `match`. The resolved value is tested against a compiled RE2 regex.

Regex semantics use Go's `regexp` package (RE2). **Regexes are not auto-anchored.** A pattern `{"regex": "origin"}` matches `"my-origin-fork"` — this is the documented footgun. Authors should use `^...$` for full-match semantics. Auto-wrapping was considered and rejected: it deviates from standard regex conventions and surprises authors who know regex.

**Validation timing.** Paths and regexes are compiled at engine construction (`rules.New`). Invalid paths (empty segments) and invalid regex syntax surface as errors there, not at evaluation time. This keeps startup-time failure messages predictable and avoids surprising log noise during traffic.

### Audit (`internal/audit`)

SQLite database using `ncruces/go-sqlite3` (WASM-based, no CGO). WAL mode for concurrent read/write. Thread-safe via mutex. Records are inserted via prepared statement for performance. The database stores request arguments and errors, but not successful tool results; this preserves auditability of broker decisions without retaining arbitrary data returned by backend tools.

The `Query` method supports:

- Tool name filtering (substring match via SQL LIKE)
- Pagination (limit/offset)
- Total count for pagination UI

### Server manager (`internal/server`)

Manages connections to backend MCP servers. At startup:

1. Connects to each configured server (stdio subprocess, HTTP, SSE, or OAuth), retrying bounded startup failures according to that backend's retry config
2. Sends MCP `initialize` handshake
3. Calls `tools/list` to discover available tools, also retrying bounded startup discovery failures
4. Prefixes each tool name as `<server>.<tool>`
5. Applies the first matching `tool_patches` entry, if any
6. Builds a registry of `<server>.<tool>` → backend mapping
7. Records backend startup status for the dashboard, including exhausted `connect` or `list_tools` failures

Tool descriptors are passed through to clients with full fidelity: in addition to name and input schema, the broker preserves each tool's `outputSchema`, `annotations` (including `title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`), and `_meta` from the upstream backend. The broker rewrites the tool name with a `<server>.` prefix for routing, may merge configured annotation patches, and may remove configured disabled tools from the registry entirely. Disabled tools are not listed in MCP `tools/list`, the dashboard tools view, or rule debugging views, and cannot be routed through the broker.

The `Backend` interface abstracts transport:

- `stdioBackend` — spawns a subprocess, communicates via stdin/stdout
- `httpBackend` — connects via Streamable HTTP, with optional OAuth after a 401 challenge
- `sseBackend` — connects via Server-Sent Events, with optional OAuth after a 401 challenge

OAuth refresh tokens for HTTP/SSE backends are stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name). OAuth callback port is deterministic per server name (FNV hash → ephemeral port range) unless the server config provides an explicit `oauth.callback_port`. Backends can also provide fixed OAuth `client_id`, `client_secret`, `scopes`, and `auth_server_metadata_url` for providers that do not support dynamic client registration or whose metadata discovery needs an explicit well-known URL; configured client IDs take precedence over stored dynamic registrations.

Streamable HTTP backends use a finite HTTP request/stream timeout so a hung backend server does not block normal backend requests forever. The timeout is configured per server with `http_timeout_seconds` and defaults to 120 seconds. Startup uses separate per-backend settings: `startup_retry_count` (default 3 retries after the first attempt, maximum 1000), `startup_retry_backoff_ms` (default 1000 ms fixed delay), and `startup_timeout_seconds` (default 10 seconds per connect or initial `tools/list` attempt). Explicit zero disables retries, backoff delay, or startup-specific timeout respectively; negative values are invalid config. Interactive OAuth is an intentional exception to the startup attempt timeout: the browser authorization flow and immediate post-authorization handshake use the broker's parent startup context so a real user login is not interrupted. The backend HTTP timeout, startup timeout, OAuth authorization window, and human approval timeout are separate.

HTTP/SSE backends use a plain client first and auto-upgrade to OAuth on 401 — they do not proactively trigger OAuth flows.

Failed backends are retried during startup, then logged and skipped rather than failing the entire startup after retries are exhausted. OAuth/auth-interactive failures are treated as non-retryable so the broker does not repeatedly open browser or callback flows; when a user authorization flow starts, it is allowed to complete under the parent startup context instead of the per-attempt startup timeout. Exhausted `tools/list` failures close and remove that backend from the active registry. Runtime rediscovery is intentionally absent; users must restart the broker after fixing an exhausted backend.

Environment variables in server config support `$VAR` expansion from the process environment, allowing secrets to be passed without hardcoding.

### Auth (`internal/auth`)

Bearer token authentication for the `/mcp` endpoint. Generates a 32-byte random token (hex-encoded, 64 chars) stored with `0600` file permissions (parent directories `0750`). The HTTP middleware validates tokens using `crypto/subtle.ConstantTimeCompare`. Token is generated on first `serve` if it doesn't already exist.

### Telegram approver (`internal/telegram`)

Optional Telegram Bot API-based approver. Uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed. When an approval is required, a message is sent to the configured chat; responses are correlated by Telegram `message_id`. Bot token and chat ID support `$VAR` expansion via `os.ExpandEnv` at startup.

### Dashboard (`internal/dashboard`)

Embedded single-page web application serving:

- **Approvals tab** — pending requests with approve/deny buttons, optional deny reason input, decided history
- **Tools tab** — backend startup status and discovered tools grouped by server; failed backends with no tools remain visible with phase, attempt count, and concise error; click a tool to see its input schema
- **Rules tab** — configured rules with the discovered tools matching each (read-only; for debugging verdicts)
- **Audit tab** — paginated audit log with tool filter, plus a live feed of incoming records. New records are prepended in real time when the view is on page 1 with no active filter and not paused; otherwise an "N new" counter appears with a "return to live view" banner. A pause toggle freezes the live feed without affecting filter or pagination state.

Real-time updates via Server-Sent Events (SSE) on a single `/events` channel. Event types are `new` (pending approval request), `removed` (request resolved), `decided` (decision applied), and `audit` (audit record written). The dashboard also implements the `Approver` interface — the `Review` method blocks until a human makes a decision via the `/api/decide` endpoint. `/api/decide` accepts an optional `reason` for denies; whitespace-only reasons are treated as no explicit reason.

### Broker (`internal/broker`)

The orchestrator. Wires together rules, approval, proxy, and audit. The `Handle` method is the single entry point for all tool calls. Interfaces:

- `ServerManager` — tool listing and call proxying
- `AuditLogger` — recording and querying audit entries
- `Approver` — human approval decisions

`MultiApprover` fans approval requests to all configured approvers (e.g., dashboard + Telegram) concurrently with a shared timeout. First response wins. Telegram denial is binary (`user`); timeout resolves as `timeout`.

### CLI (`cmd/mcp-broker`)

Cobra-based CLI with commands:

- `serve` — starts the broker (loads config, connects backends, serves HTTP)
- `config path` — prints config file location
- `config refresh` — backfills new defaults
- `config edit` — opens config in `$EDITOR`
- `token rotate` — rotates the broker bearer token
- `logout <server>` — removes stored OAuth credentials for a backend server

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

**Bearer token auth for agents, cookie auth for dashboard.** The `/mcp` endpoint requires a bearer token (32 random bytes, hex-encoded, stored with `0600` permissions). The dashboard accepts the token in the startup URL once, persists it to a session cookie (`mcp-broker-auth`, `HttpOnly`, `SameSite=Strict`), then redirects to the same dashboard path without the `token` query parameter so browsers don't keep showing the raw token.

**Failed backends don't block startup.** If one of several backend servers is unavailable, the broker retries startup connect/discovery with bounded per-backend settings, then starts with the remaining servers rather than failing entirely. Exhausted failures are logged and shown in the dashboard Tools tab. Recovery after exhaustion requires restarting the broker because runtime rediscovery and dynamic MCP tool registration are out of scope.

**Default verdict is require-approval.** Fail-closed by default — any tool not explicitly allowed requires human approval.

**Loopback-only listener, enforced at startup.** `server.ValidateLoopbackAddr` rejects any bind host that isn't a loopback IP or `localhost`. The bearer token protects against unauthorized local processes; the network boundary protects against everything else. Making network-reachability a hard error instead of a doc-only intent removes the "oops, I configured `0.0.0.0`" failure mode. Sandboxed agents reach the broker via Lima's user-mode networking, which forwards `host.lima.internal:8200` from the guest to the host's loopback — no non-loopback bind required.
