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
- **Temporary grants** — users can mint mandatory-TTL bearer grants that prepend a temporary rule overlay for a request while preserving normal broker authentication.
- **Human in the loop** — sensitive operations appear in a web dashboard where a human can approve or deny them before they execute.
- **Approval observers** — trusted startup-configured commands can receive bounded best-effort `require-approval` notifications without participating in authorization.
- **Audit trail** — tool calls are logged with arguments, verdict, approval status, denial reason, grant attribution, and errors. Audit writes are best effort so storage failure does not fail the tool pipeline, and request cancellation is propagated to SQLite. Successful tool results are not stored, keeping returned data out of the audit database.

## Architecture

```
                          ┌──────────────────────────────────────────────────────┐
                          │                    mcp-broker                       │
                          │                                                      │
Agent ──MCP(/mcp)──▶      │  ┌─────────┐   ┌──────────┐   ┌────────┐           │
                          │  │  Rules   │──▶│ Approval │──▶│ Proxy  │           │──MCP──▶ Backend A
                          │  │ Engine   │   │ fan-out  │   │ Manager│           │──MCP──▶ Backend B
                          │  └─────────┘   └────┬─────┘   └────────┘           │──MCP──▶ Backend C
                          │       │              │                              │
                          │       │              └──observer──▶ Hook Dispatcher │──exec──▶ Trusted commands
                          │       │                                             │
                          │       └──────────────────┬──────────────────────────┘
                          │                          ▼                           │
                          │                   ┌────────────┐                     │
                          │                   │   Audit    │                     │
                          │                   │  (SQLite)  │                     │
                          │                   └────────────┘                     │
                          │                                                      │
Human ──HTTP(:8200)──▶    │               Dashboard (Web UI)                    │
                          └──────────────────────────────────────────────────────┘
```

The hook branch is observational and best-effort. It is entered only when the broker commits to human approval fan-out; it has no return path into policy, approval, proxy, or audit decisions.

### Single binary, single port

mcp-broker is a single Go binary serving on a single port (default 8200):

- `/mcp` — Streamable HTTP MCP endpoint for agents, protected by a configurable request body limit (`max_request_body_bytes`, default 10 MiB)
- `/` — Web dashboard for humans (approval, tools, audit log)

### Process lifecycle

The broker uses one lifetime context for inbound HTTP requests, backend connections, and hook commands. The first `SIGINT` or `SIGTERM` cancels that context, which ends dashboard and MCP streams, pending approvals, in-flight backend work, and running hook process groups before HTTP shutdown begins. HTTP shutdown, backend closure, and database closure share one 10-second process-wide deadline; expiry force-closes HTTP and terminates the process rather than leaving a restart blocked. A second termination signal exits immediately.

Backend connections close concurrently so one slow stdio or remote backend does not delay every other backend in sequence. The hook dispatcher first linearizes into a closed state, rejects new admissions, drains queued jobs and byte reservations, and cancels running commands; its `Close` waits only within the shared shutdown context and is safe to repeat. Audit inserts and queries use caller contexts so canceled requests do not keep SQLite work alive during shutdown.

### Pipeline

Every tool call flows through the same pipeline:

1. **Grant validation and overlay** — If the request includes `Mcp-Broker-Grant`, the broker validates exactly one grant token against the durable grants DB. Duplicate, malformed, unknown, expired, revoked, or unreadable grant state fails closed before approval/proxy. A valid grant's rules are evaluated first.

2. **Rules engine** — If no grant rule matches, the broker evaluates the tool name against the ordered base rules. Each rule maps a pattern to a verdict: `allow`, `deny`, or `require-approval`. First match wins; default is `require-approval`. Deny rules may include an optional human-authored `reason`, returned to agents as `denied by rule: <reason>` or `denied by grant rule: <reason>`.

3. **Approval** — If the final verdict is `require-approval`, reject mode and the missing-approver gate run first. The broker then generates one cryptographically random 128-bit approval ID and UTC occurrence timestamp. If the initiating context is still active, it gives an `ApprovalObserver` the same immutable request immediately before invoking the human approver. The call then appears in the web dashboard and fans out to optional Telegram; a human approves or denies it. Dashboard denials can include an optional reason, returned as `denied by user: <reason>`; binary denials return `denied by user`. Approval timeouts return `denied by timeout`. If ID generation fails, authorization fails closed. Per-request `Mcp-Broker-Approval-Mode: reject` skips observer and approver fan-out and immediately rejects calls whose final verdict is `require-approval`; `allow` and `deny` verdicts are unchanged. Observer delivery is never an authorization input.

4. **Proxy** — The call is forwarded to the backend MCP server that owns the tool. The broker strips the namespace prefix before forwarding.

5. **Audit** — The broker attempts to record every call in a SQLite database with: timestamp, tool name, arguments, verdict, approval status, denial reason, grant metadata/status, rule source, and any error. Writes use the request context and are best effort: cancellation or a database failure can omit a row without failing the tool call. Successful tool results are deliberately not stored.

### Tool namespacing

Each backend server has a name (from config). When tools are discovered, they are prefixed with `<server-name>.` to avoid collisions. For example, a server named `github` with a tool `search` becomes `github.search`.

## Components

### Config (`internal/config`)

Primary config lives at `~/.config/mcp-broker/config.json`; base policy rules live in a separate `rules.json` document by default. `config.json` contains backend/server settings and `rules.path`, while `rules.json` contains `{ "rules": [...] }`. On first run, default config and rules files are written. The `Refresh` function loads, overlays defaults for new fields, writes config back, and ensures the companion rules file exists — useful for upgrading config after new features are added.

Most config is loaded once at startup. Policy rule content is the only hot-reloadable configuration: sending `SIGHUP` to the running process reads the effective rules file selected at startup, compiles a complete rules snapshot, and atomically swaps it into the broker and dashboard after validation succeeds. Invalid reloads are non-fatal and leave the previous rules active. Changing `rules.path` itself requires restart.

The legacy top-level `rules_path` field remains an input-only compatibility alias. `rules.path` takes precedence when both are present, and config refresh serializes only the canonical nested field. Legacy top-level `config.json` rule arrays are migration-only. If legacy embedded rules exist and the effective rules file is missing, they are written to `rules.json`. If both exist, the rules file is authoritative and legacy embedded rules are ignored with a warning.

Backend servers, `tool_patches`, hook definitions/dispatch limits, listener settings, audit path, grants path/max TTL, auth token, Telegram settings, approval timeout, log level, browser opening, and request body limit remain startup-only and require restart. Tool patches are ordered load-time transforms for discovered tools. Tool patch patterns match broker-prefixed tool names with `filepath.Match`; the first matching patch applies. `disabled: true` removes the tool from the broker registry, and `annotations` merges field-by-field into MCP tool annotations (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`). `_meta` is passed through unchanged and is not patched.

Defaults:

- Host: `127.0.0.1` (must resolve to a loopback interface — validated at startup)
- Port: 8200
- Rules path: `rules.json` alongside the effective config file
- Rules: `[{"tool": "*", "verdict": "require-approval"}]` in the separate rules document
- Audit path: `~/.local/share/mcp-broker/audit.db`
- Grants path: `~/.local/share/mcp-broker/grants.db`
- Maximum grant TTL: 604800 seconds (7 days)
- Log level: `info`
- Hook command concurrency: 4
- Hook queue capacity: 64 jobs
- Maximum hook payload: 10 MiB
- Hook queued-payload budget: 64 MiB
- `require-approval` hook handlers: none

### Rules engine (`internal/rules`)

Stateless evaluator plus a reloadable store. The evaluator takes a list of `RuleConfig` at construction time. `Evaluate(tool, args)` walks rules in order and returns the first matching verdict. The store holds one immutable evaluator snapshot and atomically swaps in a new precompiled snapshot on successful reload. Broker evaluation returns both the verdict and matched rule metadata from the same snapshot so deny reasons and audit verdicts cannot cross a reload boundary. Uses `filepath.Match` for glob matching, which supports `*` (single segment) and `?` wildcards.

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

### Grants (`internal/grants`)

Durable authorization overlay state stored in a separate SQLite database from audit. The CLI writes directly to this DB so `grant mint`, `grant list`, and `grant revoke` work while the broker is offline. The running broker opens the same DB and validates a supplied grant token on every MCP request, so new grants and revocations take effect without restart or `SIGHUP`.

Grant tokens are high-entropy bearer secrets. Only a SHA-256 token hash and short fingerprint are stored; raw tokens are returned only by `grant mint` and cannot be recovered. Grant records contain stable ID, name, description, fingerprint, JSON rules, created/expires timestamps, and optional revoked timestamp. Expired and revoked grants are retained for auditability. Grant rules are normal `RuleConfig` objects and are compiled with the same rules engine used for base policy.

### Audit (`internal/audit`)

SQLite database using `ncruces/go-sqlite3` (WASM-based, no CGO). WAL mode for concurrent read/write. Thread-safe via mutex. Records are inserted via prepared statement for performance. The database stores request arguments, decision attribution (`grant_id`, `grant_name`, `grant_fingerprint`, `grant_status`, `rule_source`), and errors, but not successful tool results; this preserves auditability of broker decisions without retaining arbitrary data returned by backend tools.

The `Query` method supports:

- Tool name filtering (substring match via SQL LIKE)
- Decision-source filtering using the dashboard's compact source labels (`base`, `grant`, `fall-through`, `grant-error`)
- Status filtering (`success` or `error`)
- Verdict filtering (`allow`, `deny`, or `require-approval`)
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

Streamable HTTP backends use a finite HTTP request/stream timeout so a hung backend server does not block normal backend requests forever. The timeout is configured per server with `http_timeout_seconds` and defaults to 120 seconds. If a backend restart or session expiry invalidates the negotiated MCP session, the broker creates and initializes a fresh client and retries the rejected `tools/list` or tool call once. Concurrent callers share the replacement client rather than starting independent reconnects. Startup uses separate per-backend settings: `startup_retry_count` (default 3 retries after the first attempt, maximum 1000), `startup_retry_backoff_ms` (default 1000 ms fixed delay), and `startup_timeout_seconds` (default 10 seconds per connect or initial `tools/list` attempt). Explicit zero disables retries, backoff delay, or startup-specific timeout respectively; negative values are invalid config. Interactive OAuth is an intentional exception to the startup attempt timeout: startup and runtime browser authorization flows use the broker's parent context with a dedicated five-minute limit, while the immediate post-authorization handshake uses the parent context and transport timeout. The backend HTTP timeout, startup timeout, OAuth authorization window, and human approval timeout are separate.

HTTP/SSE backends use a plain client first and auto-upgrade to OAuth on 401 — they do not proactively trigger OAuth flows.

Failed backends are retried during startup, then logged and skipped rather than failing the entire startup after retries are exhausted. OAuth/auth-interactive failures are treated as non-retryable so the broker does not repeatedly open browser or callback flows; a user authorization flow runs under the parent context with its dedicated five-minute limit instead of the per-attempt startup timeout. Exhausted `tools/list` failures close and remove that backend from the active registry. Runtime rediscovery is intentionally absent; users must restart the broker after fixing an exhausted backend.

Environment variables in server config support `$VAR` expansion from the process environment, allowing secrets to be passed without hardcoding.

### Auth (`internal/auth`)

Bearer token authentication for the `/mcp` endpoint. Generates a 32-byte random token (hex-encoded, 64 chars) stored with `0600` file permissions (parent directories `0750`). The HTTP middleware validates tokens using `crypto/subtle.ConstantTimeCompare`. Token is generated on first `serve` if it doesn't already exist.

### Hooks (`internal/hooks`)

The hooks subsystem implements `broker.ApprovalObserver`; it is deliberately separate from `Approver` because command results must have no authorization semantics. V1 registers only `require-approval`. The broker-owned request contains the shared ID/time, tool and complete input snapshot source, final verdict/rule source, and optional non-secret valid-grant metadata. The dispatcher serializes a versioned JSON object plus a trailing newline synchronously, normalizes nil input to `{}`, and never adds authentication or grant tokens. Unsupported/cyclic JSON and oversized payloads are metadata-only drops.

At startup, handler `$VAR`/`${VAR}` environment references are expanded once and overlaid on the inherited broker environment; config load/refresh retains the raw references. Commands use direct executable-plus-argv invocation. Request-controlled values are written only to stdin and are never interpolated into executable names, arguments, or environment. Bare executable lookup uses the broker `PATH`; a configured child `PATH` overlay does not change lookup.

Admission is a mutex-linearized non-blocking try-send that reserves both one queue slot and serialized payload bytes. Each configured handler receives at most one attempt and jobs are never retried. Queue-slot saturation, byte-budget saturation, serialization/size failure, deadline expiry, and shutdown drop jobs independently without affecting approval. Handler deadlines begin at admission, so queue delay consumes runtime. The queued budget plus `max_concurrent * max_payload_bytes` bounds retained serialized payload memory.

Workers recheck lifetime cancellation and the end-to-end deadline immediately before starting a command. Stdout and stderr are left on `os/exec`'s direct operating-system null-device path, avoiding broker-owned copy pipes; only metadata classifications are logged. On macOS/Linux the runner creates a new process group, sends TERM on deadline or lifetime cancellation, escalates to KILL after a fixed short grace period, and always waits for the direct child. Same-group descendants are covered; escaped descendants are not. Other targets compile a direct-process fallback with no descendant guarantee.

### Telegram approver (`internal/telegram`)

Optional Telegram Bot API-based approver. Uses long-polling (`getUpdates?timeout=30`) — no inbound connections needed. When an approval is required, a message is sent to the configured chat; responses are correlated by Telegram `message_id`. Bot token and chat ID support `$VAR` expansion via `os.ExpandEnv` at startup.

### Dashboard (`internal/dashboard`)

Embedded single-page web application serving:

- **Approvals tab** — pending requests with approve/deny buttons, optional deny reason input, decided history
- **Tools tab** — backend startup status and discovered tools grouped by server; failed backends with no tools remain visible with phase, attempt count, and concise error; click a tool to see its input schema
- **Rules tab** — active rules with the discovered tools matching each (read-only; for debugging verdicts; reflects successful `SIGHUP` rules reloads)
- **Grants tab** — read-only active/expired/revoked grant metadata, timestamps, display fingerprint, and compact rules summary. It intentionally has no creation, editing, or revocation controls in v1.
- **Audit tab** — paginated audit log with compact filters for tool substring, source (`base`, `grant`, `fall-through`, `grant-error`), status, and verdict. The table shows compact source labels while expanded rows show full grant/rule-source attribution. New matching records are prepended in real time when the view is on page 1 and not paused; otherwise an "N new" counter appears with a "return to live view" banner. A pause toggle freezes the live feed without affecting filter or pagination state.

Real-time updates via Server-Sent Events (SSE) on a single `/events` channel. Event types are `new` (pending approval request), `removed` (request resolved), `decided` (decision applied), and `audit` (audit record written). The dashboard also implements the `Approver` interface — the `Review` method blocks until a human makes a decision via the `/api/decide` endpoint. `/api/decide` accepts an optional `reason` for denies; whitespace-only reasons are treated as no explicit reason.

### Broker (`internal/broker`)

The orchestrator. Wires together rules, approval, proxy, and audit. The `Handle` method is the single entry point for all tool calls. Interfaces:

- `ServerManager` — tool listing and call proxying
- `AuditLogger` — recording and querying audit entries
- `Approver` — human approval decisions
- `GrantValidator` — per-request durable grant validation
- `ApprovalObserver` — non-authorizing best-effort observation of a broker-owned `ApprovalRequest`

`ApprovalRequest` is generated only after the broker commits an active request to human fan-out. It carries one ID/time and the selected policy/grant attribution so rules reloads cannot change an in-progress decision and dashboard/Telegram fan-out cannot duplicate hook emission. `MultiApprover` fans approval requests to all configured approvers (e.g., dashboard + Telegram) concurrently with a shared timeout. First response wins. Telegram denial is binary (`user`); timeout resolves as `timeout`.

### CLI (`cmd/mcp-broker`)

Cobra-based CLI with commands:

- `serve` — starts the broker (loads config, connects backends, serves HTTP; handles `SIGHUP` for rules-only reload)
- `config path` — prints config file location
- `config refresh` — backfills new defaults and ensures the rules file exists
- `config edit` — opens config in `$EDITOR`
- `rules path` — prints the effective base rules file location
- `rules refresh` — writes the base rules file in canonical form
- `rules edit` — opens the base rules file in `$EDITOR`
- `token rotate` — rotates the broker bearer token
- `grant mint --name <name> --ttl <duration> --rules-file <path-or-> [--description <text>]` — creates a mandatory-TTL grant and prints the raw token once
- `grant list` — lists retained grant metadata without tokens or hashes
- `grant revoke <grant-id-or-fingerprint>` — durably revokes a grant by stable ID or unambiguous fingerprint
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

**Rules reload without backend churn.** Rules are frequent operational edits, so `SIGHUP` reloads only the selected `rules.json` policy document and keeps the HTTP server, backend connections, discovered tools, and listener address unchanged. Reload compiles before swapping and keeps the old snapshot on any error. The `rules.path` setting itself is startup-only and requires restart.

**Default verdict is require-approval.** Fail-closed by default — any tool not explicitly allowed requires human approval.

**Hooks observe approval commitment, never policy.** A hook attempt occurs only after the final `require-approval` verdict passes reject/no-approver gates and an active initiating context reaches fan-out. A cancellation racing after that check can remove the pending dashboard request after admission; notification is not proof of a live prompt. Hook output, status, timeout, overload, and shutdown have no path into approval, proxy, or audit decisions.

**Hook delivery is bounded at-most-once.** The dispatcher is in-memory and non-durable: no retries, outbox, dead-letter queue, or delivery guarantee. This keeps admission independent of queue capacity and command execution while bounding retained payload memory. Hook commands are privileged trusted host configuration, run unsandboxed as the broker user, inherit its environment, and receive full unredacted tool input; isolation or secret confinement is not promised.

**Grants are prepend-only overlays.** A supplied valid grant is evaluated before base rules, and base rules run only if no grant rule matches. Grant rules support `allow`, `deny`, and `require-approval`, so a grant can intentionally shadow a base deny for a bounded TTL. Invalid, expired, revoked, duplicate, or unreadable grant state fails closed and is audited; silent fallback to base rules is not allowed for bad grant headers.

**Grant tokens are hash-only at rest.** The raw grant token is a bearer secret printed once by `grant mint`; the DB stores only a SHA-256 hash and display fingerprint. Dashboard, list/revoke CLI output, audit rows, and logs must not expose raw tokens or full hashes.

**Loopback-only listener, enforced at startup.** `server.ValidateLoopbackAddr` rejects any bind host that isn't a loopback IP or `localhost`. The bearer token protects against unauthorized local processes; the network boundary protects against everything else. Making network-reachability a hard error instead of a doc-only intent removes the "oops, I configured `0.0.0.0`" failure mode. Sandboxed agents reach the broker via Lima's user-mode networking, which forwards `host.lima.internal:8200` from the guest to the host's loopback — no non-loopback bind required.
