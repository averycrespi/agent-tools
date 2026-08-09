# MCP Broker

An MCP proxy that lets sandboxed agents use external tools without holding secrets.

Agents run in sandboxes with no credentials and restricted network access — but they still need to call GitHub, Jira, Slack, and other external APIs. mcp-broker runs on the host, holds the secrets, and exposes backend MCP servers through a single endpoint. Policy rules control which tools are allowed, sensitive operations require human approval via a web dashboard, and calls are audit-logged on a best-effort basis.

## How it works

```
Agent ──MCP──▶ mcp-broker ──MCP──▶ Backend servers
                  │
                  ├─ Optional grant overlay (Mcp-Broker-Grant)
                  ├─ Rules engine (glob-based allow/deny/require-approval)
                  ├─ Human approval via web dashboard + optional Telegram
                  ├─ Best-effort require-approval command hooks (optional)
                  └─ SQLite audit log
```

An agent connects to mcp-broker as a single MCP server. mcp-broker connects to one or more backend MCP servers (via stdio or HTTP), discovers their tools, and re-exposes them with `<server>.<tool>` namespacing. Every tool call flows through the pipeline:

1. **Grant overlay** — if the request supplies `Mcp-Broker-Grant`, the broker validates that durable grant token and evaluates its rules first.
2. **Base rules check** — if no grant rule matches, glob patterns match tool names to base verdicts (`allow`, `deny`, `require-approval`).
3. **Approval** — if the final verdict is `require-approval`, the broker creates one approval request, makes best-effort non-blocking notification-hook admissions, then blocks until a human approves or denies it via the web dashboard (and optionally Telegram). A configurable timeout (default 10 minutes) auto-denies if no response arrives. Requests can opt out of waiting with `Mcp-Broker-Approval-Mode: reject`. Hooks observe this transition but never participate in authorization.
4. **Proxy** — the call is forwarded to the backend server.
5. **Audit** — the broker attempts to record the call, verdict, grant attribution, and any error in SQLite. Audit storage failure or cancellation can omit a row but does not fail the tool call.

## Security

mcp-broker is designed for **local use only**. Startup refuses to bind anything but a loopback interface (`127.0.0.1`, `::1`, or `localhost`) — binding to `0.0.0.0` or a LAN IP is a hard error, not a warning. This is the load-bearing security boundary; the bearer token is defense-in-depth on top of it.

**Threat model:** Prevent other processes on your machine from calling the broker's HTTP endpoints without authorization. This covers casual/accidental access and opportunistic localhost attacks, not a determined attacker with root access to your machine.

**What auth provides:**

- Two distinct bearer credentials: `agent-token` authorizes only exact `/mcp`; `admin-token` authorizes the dashboard, its assets/APIs/SSE/cookie bootstrap, and root/catch-all redirects
- Public `/healthz` and `/dashboard/unauthorized` endpoints for liveness and login guidance
- An admin-only `HttpOnly`, `SameSite=Strict` dashboard cookie
- Constant-time credential comparison

Neither role is a superset of the other. Never put `admin-token` in a sandbox.

**What auth does NOT provide:**

- Protection against an attacker who can read the corresponding credential file
- TLS/encryption (traffic is plain HTTP on localhost)
- Per-agent identities, user accounts, or automatic rotation

**Sandboxed agents** reach the broker via Lima's user-mode networking, which forwards guest connections to `host.lima.internal:8200` to the host's loopback. Set `MCP_BROKER_URL=http://host.lima.internal:8200/mcp` inside the sandbox.

## Quick start

```bash
# Build
make build

# Run (creates default config on first run)
./mcp-broker serve

# An interactive terminal prints and may open an admin-token dashboard URL.
# Non-interactive startup never prints or opens a token-bearing URL.
# MCP endpoint at http://localhost:8200/mcp (requires the agent Bearer token)
# Liveness endpoint at http://localhost:8200/healthz returns "ok" without auth
```

## Health check

`GET /healthz` is an unauthenticated liveness check for local supervisors. It returns `200 OK` with `ok\n` when the broker HTTP server is running. It does not check backend MCP servers; degraded or exhausted backends are shown in the dashboard Tools tab instead.

## Configuration

Config lives at `~/.config/mcp-broker/config.json` (or `$XDG_CONFIG_HOME/mcp-broker/config.json`). Base policy rules live in a separate rules file, defaulting to `rules.json` alongside `config.json`.

```json
{
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "$GITHUB_TOKEN" }
    },
    "github-remote": {
      "type": "sse",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": { "Authorization": "Bearer $GITHUB_TOKEN" }
    },
    "internal": {
      "type": "streamable-http",
      "url": "http://localhost:3000/mcp",
      "http_timeout_seconds": 120,
      "startup_retry_count": 3,
      "startup_retry_backoff_ms": 1000,
      "startup_timeout_seconds": 10
    },
    "oauth-remote": {
      "type": "streamable-http",
      "url": "https://example.com/mcp",
      "oauth": {
        "client_id": "$MCP_OAUTH_CLIENT_ID",
        "callback_port": 3118,
        "auth_server_metadata_url": "https://example.com/.well-known/oauth-authorization-server"
      }
    }
  },
  "rules": {
    "path": "/Users/alice/.config/mcp-broker/rules.json"
  },
  "tool_patches": [
    {
      "tool": "github.search_*",
      "annotations": {
        "readOnlyHint": true,
        "destructiveHint": false,
        "idempotentHint": true,
        "openWorldHint": true
      }
    },
    { "tool": "github.delete_*", "disabled": true }
  ],
  "host": "127.0.0.1",
  "port": 8200,
  "approval_timeout_seconds": 600,
  "max_request_body_bytes": 10485760,
  "telegram": {
    "enabled": false,
    "token": "$TELEGRAM_BOT_TOKEN",
    "chat_id": "$TELEGRAM_CHAT_ID"
  },
  "hooks": {
    "dispatch": {
      "max_concurrent": 4,
      "queue_size": 64,
      "max_payload_bytes": 10485760,
      "max_queued_bytes": 67108864
    },
    "events": {
      "require-approval": []
    }
  },
  "audit": {
    "path": "/Users/alice/.local/share/mcp-broker/audit.db"
  },
  "grants": {
    "path": "/Users/alice/.local/share/mcp-broker/grants.db",
    "max_ttl_seconds": 604800
  },
  "log": {
    "level": "info"
  }
}
```

### Top-level settings

| Field                      | Description                                                                              |
| -------------------------- | ---------------------------------------------------------------------------------------- |
| `host`                     | Listen host. Must resolve to loopback. Defaults to `127.0.0.1`.                          |
| `port`                     | Listen port. Defaults to `8200`.                                                         |
| `approval_timeout_seconds` | Human approval timeout. Defaults to 600 seconds.                                         |
| `max_request_body_bytes`   | Maximum accepted request body size on `/mcp`. Defaults to 10 MiB; set to `0` to disable. |
| `open_browser`             | Open the dashboard in a browser on startup. Defaults to `true`.                          |
| `rules.path`               | Base policy rules file. Defaults to `rules.json` alongside the effective config file.    |
| `grants.path`              | SQLite grants DB path. Defaults to `$XDG_DATA_HOME/mcp-broker/grants.db`.                |
| `grants.max_ttl_seconds`   | Maximum mintable grant TTL. Defaults to 604800 seconds (7 days); must be positive.       |

### Reloading rules and role credentials

Policy rules plus `agent-token` and `admin-token` can be reloaded without restarting the listener or reconnecting backend MCP servers. After editing a reloadable file, send `SIGHUP` to the running process:

```bash
kill -HUP $(pgrep -x mcp-broker)
```

For the launchd setup, use:

```bash
launchctl kill HUP gui/$UID/dev.agent-tools.mcp-broker
```

Rules and authentication reload independently: a broken rules file or invalid/missing credential cannot block a valid safe change to the other state. An invalid, equality-causing, or prior-opposite-role candidate retains that role's previous in-memory value; the pair is published atomically. New requests use successful changes, while decisions already made keep their rules snapshot.

Restart mcp-broker for changes to `servers`, `tool_patches`, `hooks`, `host`, `port`, `rules.path`, `audit.path`, `grants.path`, `grants.max_ttl_seconds`, Telegram approver settings, approval timeout, log level, `open_browser`, or `max_request_body_bytes`, and after fixing a backend that exhausted startup retries. Minted and revoked grant records take effect on the next MCP request without restart because the broker checks the grants DB per request.

### Servers

Servers is a map keyed by server name. Each name is used as a tool prefix (e.g. `github.search`).

| Field                      | Description                                                                                                                                                              |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `command`                  | Command to spawn (stdio transport, default)                                                                                                                              |
| `args`                     | Command arguments                                                                                                                                                        |
| `env`                      | Environment variables; `$VAR` and `${VAR}` references are expanded from the process environment                                                                          |
| `type`                     | Transport type: omit for stdio, `"streamable-http"` for Streamable HTTP, `"sse"` for SSE                                                                                 |
| `url`                      | URL for HTTP/SSE transport                                                                                                                                               |
| `headers`                  | HTTP headers; `$VAR` and `${VAR}` references are expanded from the process environment                                                                                   |
| `oauth`                    | Optional OAuth client settings for HTTP/SSE backends that require a fixed client ID, client secret, scopes, or callback port.                                            |
| `http_timeout_seconds`     | Streamable HTTP backend request/stream timeout. Defaults to 120 seconds when omitted.                                                                                    |
| `startup_retry_count`      | Startup retries after the first connect or `tools/list` attempt. Defaults to 3. Set `0` for one attempt only. Negative values and values above 1000 are invalid.         |
| `startup_retry_backoff_ms` | Fixed delay between startup attempts. Defaults to 1000 ms. Set `0` for no delay. Negative values are invalid.                                                            |
| `startup_timeout_seconds`  | Per-attempt startup timeout for connect and initial `tools/list`. Defaults to 10 seconds. Set `0` to disable this startup-specific timeout. Negative values are invalid. |

Streamable HTTP backends automatically recreate their MCP client and retry once when a backend restart or session expiry invalidates the current session; the broker itself does not need to be restarted. Startup retry settings are per backend and apply independently to the connect and initial `tools/list` phases. Worst-case serial startup delay is roughly the sum, across configured backends, of up to `2 * ((startup_retry_count + 1) * startup_timeout_seconds + startup_retry_count * startup_retry_backoff_ms)`, plus backend work that is not bounded when `startup_timeout_seconds` is `0`. `startup_timeout_seconds` is intentionally not the timeout for interactive OAuth: startup and runtime browser authorization flows use the broker's parent context with a dedicated five-minute limit, while the immediate post-authorization handshake uses the parent context and transport timeout. `http_timeout_seconds` still controls normal Streamable HTTP backend requests after startup; it is not the startup retry timeout.

If a backend exhausts startup retries, mcp-broker logs the failure, continues serving MCP and dashboard endpoints with the remaining healthy backends, and shows the failed backend in the dashboard Tools tab with its failed phase, attempt count, and concise error. Runtime rediscovery is not implemented: after fixing an exhausted backend, restart mcp-broker to discover its tools.

### OAuth

OAuth is handled automatically. When a server responds with HTTP 401, the broker runs an OAuth flow (PKCE, browser-based authorization). For servers that support dynamic client registration, no OAuth configuration is needed: the broker registers a client and stores that client registration plus tokens in the OS keychain (macOS Keychain / Linux Secret Service).

Servers that do not support dynamic client registration can provide fixed OAuth client settings:

```json
{
  "servers": {
    "oauth-remote": {
      "type": "streamable-http",
      "url": "https://example.com/mcp",
      "oauth": {
        "client_id": "$MCP_OAUTH_CLIENT_ID",
        "callback_port": 3118,
        "auth_server_metadata_url": "https://example.com/.well-known/oauth-authorization-server"
      }
    }
  }
}
```

Supported `oauth` fields are `client_id`, `client_secret`, `callback_port`, `scopes`, and `auth_server_metadata_url`. `client_id`, `client_secret`, and `auth_server_metadata_url` support `$VAR` / `${VAR}` expansion from the process environment. `callback_port` controls the local redirect URI `http://localhost:<port>/callback`; when omitted, the broker uses a deterministic per-server port.

Interactive OAuth is deliberately exempt from `startup_timeout_seconds` and from repeated startup retries. Startup and runtime browser authorization flows instead have a dedicated five-minute limit under the broker's parent context, so a per-attempt startup timeout does not interrupt login and an abandoned flow cannot wait indefinitely. If the OAuth flow times out, is cancelled, or is denied, that backend is marked failed and shown in the dashboard.

If a backend's cached login goes stale — for example after the upstream rotates its OAuth client registration and tool calls start failing with authorization errors — clear it with `mcp-broker logout <server>`. This removes the server's stored token and client registration from the keychain; the next call triggers a fresh OAuth flow.

### Approval command hooks

Hooks are startup-configured, asynchronous observers for approval notifications. V1 supports only the `require-approval` event:

```json
{
  "hooks": {
    "dispatch": {
      "max_concurrent": 4,
      "queue_size": 64,
      "max_payload_bytes": 10485760,
      "max_queued_bytes": 67108864
    },
    "events": {
      "require-approval": [
        {
          "command": "/Users/alice/bin/notify-approval.sh",
          "args": ["agent-approvals"],
          "timeout_seconds": 10,
          "env": { "NOTIFICATION_TOKEN": "$NOTIFICATION_TOKEN" }
        }
      ]
    }
  }
}
```

Omitting `hooks`, using `"hooks": {}`, or configuring an empty event list starts no hook workers and preserves normal broker behavior. `mcp-broker config refresh` writes the canonical defaults and empty event list. Hook changes require a broker restart; `SIGHUP` reloads policy rules plus both role credentials.

Each handler is a direct executable plus argument vector. The broker does not invoke a shell or substitute request data into the command or arguments. To use shell syntax deliberately, configure a shell explicitly, for example `"command": "/bin/sh", "args": ["-c", "..."]`. Relative executable paths use the broker working directory. Bare names are resolved through the broker process's startup `PATH`; a handler's `PATH` overlay affects the child environment, not executable lookup.

Handlers inherit the broker environment and overlay their configured `env`. `$VAR` and `${VAR}` references are preserved literally in `config.json` and expanded once when the dispatcher is constructed. Environment keys must match `[A-Za-z_][A-Za-z0-9_]*`. Commands, arguments, keys, and values containing NUL are rejected. Every handler must set `timeout_seconds` from 1 through 86400.

The broker emits only after the final policy verdict is `require-approval`, reject mode and the missing-approver gate have passed, and the initiating context is still active. It creates one 128-bit request ID and UTC occurrence time, attempts each configured handler once immediately before dashboard/Telegram fan-out, publishes both in dashboard pending/SSE data, and accepts that same ID at `/api/decide`. Allowed, denied, reject-mode, no-approver, invalid-grant, and already-canceled calls do not emit. Cancellation can race after the pre-emission check, so hook delivery is not proof that a dashboard request remains pending. Once admitted, a hook uses broker-lifetime and handler-timeout cancellation rather than client-request cancellation.

An admitted command receives one UTF-8 JSON object followed by a newline on standard input:

```json
{
  "schema_version": 1,
  "hook_event_name": "require-approval",
  "request_id": "c9cb427bc1387a23c9cb427bc1387a23",
  "occurred_at": "2026-08-03T18:42:00Z",
  "tool_name": "github.create_pull_request",
  "tool_input": { "owner": "example", "repo": "project" },
  "policy": {
    "verdict": "require-approval",
    "rule_source": "base"
  },
  "grant": {
    "id": "optional-id",
    "name": "optional-name",
    "fingerprint": "optional-fingerprint",
    "status": "active"
  }
}
```

`tool_input` is a complete JSON-semantic snapshot; nil input is `{}`. `grant` is omitted when no valid grant was supplied and remains present when a valid grant falls through to base/default policy. Authentication and raw grant tokens are never dedicated payload fields. The unredacted tool input can itself contain caller-supplied secrets.

Dispatch is bounded, in-memory, best-effort, at-most-once, and non-retrying. Admission never waits for queue capacity or command completion. A handler job can be dropped independently because serialization failed, the payload exceeded its limit, the queue or byte budget was saturated, its admission-to-completion deadline expired, or shutdown began; drops never change approval. Serialization and ID generation happen synchronously before fan-out. Defaults and validation ranges are:

| Setting                   | Default  | Valid range                 |
| ------------------------- | -------- | --------------------------- |
| `max_concurrent`          | 4        | 1–64 commands               |
| `queue_size`              | 64       | 1–4096 queued jobs          |
| `max_payload_bytes`       | 10 MiB   | 1 byte–64 MiB               |
| `max_queued_bytes`        | 64 MiB   | `max_payload_bytes`–512 MiB |
| handler `timeout_seconds` | required | 1–86400 seconds             |

Queue delay consumes the handler timeout, so expired queued work does not start. Retained payload memory is bounded by `max_queued_bytes + max_concurrent * max_payload_bytes`. This timeout is independent of human `approval_timeout_seconds` and backend `http_timeout_seconds`.

Stdout and stderr go directly to the operating system's discard sink and are neither buffered nor interpreted. Exit status and output cannot approve, deny, mutate, audit, or proxy a call. Logs contain only event name, handler index, request ID, duration/status, and drop/timeout classification—not command, arguments, environment, tool input, output, or raw subprocess errors.

On macOS and Linux, each hook starts in a new process group. Timeout or broker shutdown sends the group a termination signal, escalates after a short bounded grace period, and waits for the direct child; same-group descendants are covered, while descendants that deliberately leave the group are not. Other platforms use a direct-process fallback with no descendant guarantee. Shutdown rejects new work, discards queued jobs, cancels running jobs, and waits only within the process-wide shutdown deadline.

**Security:** Hook definitions are trusted host configuration that execute unsandboxed code as the broker user. Commands receive the broker's inherited environment—which may contain secrets—and full unredacted tool input, and can read files, launch processes, persist data, or exfiltrate it. Configure only trusted commands; hooks do not isolate host code from the broker.

### Mobile Approval (Telegram)

To receive approval requests on your phone and approve/deny them from anywhere, enable the Telegram notifier:

```json
{
  "approval_timeout_seconds": 600,
  "telegram": {
    "enabled": true,
    "token": "$TELEGRAM_BOT_TOKEN",
    "chat_id": "$TELEGRAM_CHAT_ID"
  }
}
```

`token` and `chat_id` support `$VAR` / `${VAR}` environment variable expansion.

**Setup:**

1. Create a bot via [@BotFather](https://t.me/BotFather) on Telegram — it gives you a bot token.
2. Start a chat with your bot, then get your chat ID by calling `https://api.telegram.org/bot<TOKEN>/getUpdates` after sending any message to it.
3. Set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` in your environment and set `enabled: true` in config.

When enabled, approval requests are sent to both the web dashboard and Telegram simultaneously. Either can resolve the request — the first response wins. The web dashboard shows a live countdown; Telegram messages are updated to show the outcome after a decision is made. Dashboard denials can include an optional reason, returned to the agent as `denied by user: <reason>`; Telegram denials are binary and return `denied by user`.

### Rules

Base policy rules live in `~/.config/mcp-broker/rules.json` by default, or the file named by `rules.path` in `config.json`. The legacy top-level `rules_path` field remains accepted; if both fields are present, `rules.path` wins. Running `mcp-broker config refresh` rewrites the legacy field in the canonical nested form. The canonical rules file shape is:

```json
{
  "rules": [
    { "tool": "github.search_*", "verdict": "allow" },
    { "tool": "github.push*", "verdict": "require-approval" },
    { "tool": "*", "verdict": "require-approval" }
  ]
}
```

Rules are evaluated top-to-bottom, first match wins. Patterns use Go's `filepath.Match` glob syntax.

| Verdict            | Behavior                                      |
| ------------------ | --------------------------------------------- |
| `allow`            | Tool call proceeds immediately                |
| `deny`             | Tool call is rejected                         |
| `require-approval` | Tool call blocks until approved via dashboard |

Default (no matching rule): `require-approval`. On first run, mcp-broker creates `rules.json` with a catch-all `require-approval` rule. Existing configs with legacy embedded `rules` are migrated into `rules.json` when that file is missing. If both legacy embedded rules and `rules.json` exist, `rules.json` is authoritative and the legacy rules are ignored with a warning. Denials are returned to agents as MCP tool errors: `denied by rule`, `denied by rule: <reason>`, `denied by user`, `denied by user: <reason>`, or `denied by timeout`.

The approval timeout is separate from `http_timeout_seconds`: approval waiting can last up to `approval_timeout_seconds`, then approved Streamable HTTP backend calls are bounded by the backend's HTTP timeout.

#### Request approval mode

MCP tool call requests may include `Mcp-Broker-Approval-Mode` to control what happens when a rule returns `require-approval`:

| Header value | Behavior                                                     |
| ------------ | ------------------------------------------------------------ |
| `wait`       | Default. Queue the call for dashboard/Telegram approval.     |
| `reject`     | Do not queue approval. Return an MCP tool error immediately. |

`reject` does not bypass rules. `allow` and `deny` verdicts behave normally; only `require-approval` changes. Rejected calls are audited with verdict `require-approval`, approval `false`, denial reason `approval-mode: reject`, and appear in the dashboard audit table as `rejected`.

### Grants

Grants are temporary bearer tokens that prepend a small rule set to the normal policy for one request. They are useful when you want to intentionally allow or deny a narrow workflow for a bounded time without editing the base rules. Grants are authorization overlays, not authentication: clients must still send the normal `Authorization: Bearer <agent-token>` header.

Mint a grant offline with a required name, TTL, and JSON rules file:

```bash
mcp-broker grant mint --name release-window --ttl 2h --rules-file grant-rules.json
```

The rules file may be either a bare array of normal rule objects or an object with a top-level `rules` array:

```json
{
  "rules": [
    {
      "tool": "github.create_pull_request",
      "verdict": "allow",
      "reason": "release window"
    },
    { "tool": "git.push", "verdict": "require-approval" }
  ]
}
```

The full token is printed exactly once by `grant mint`; store it securely. The grants DB stores only a SHA-256 token hash plus a short display fingerprint, never the raw token. `grant list`, `grant revoke`, dashboard APIs, and audit rows show metadata such as grant ID, name, fingerprint, status, timestamps, and compact rules, but not the token or full hash.

Use a grant on an MCP tool call by adding exactly one header:

```text
Authorization: Bearer <agent-token>
Mcp-Broker-Grant: <grant-token>
```

Duplicate, empty, comma-combined, malformed, unknown, expired, and revoked grant headers fail closed before approval or proxying. Valid grants evaluate first. If a grant rule matches, its verdict is final and may intentionally shadow a base rule, including a base `deny`; if no grant rule matches, the broker falls through to base rules. `Mcp-Broker-Approval-Mode: reject` is applied only after this final verdict is selected, so it only changes final `require-approval` decisions whether they came from grant or base rules.

List and revoke retained grants offline:

```bash
mcp-broker grant list
mcp-broker grant revoke <grant-id-or-fingerprint>
```

Revocation is durable and idempotent, and a running broker observes it on the next MCP request. Expired and revoked grants are retained for visibility. The dashboard Grants tab is read-only in v1; grant creation and revocation remain CLI-only. Audit records include grant ID/name/fingerprint/status and rule source (`grant`, `base`, or `none/default`) so you can distinguish grant matches from grant fallthrough to base policy.

#### Argument matching

Deny rules may include an optional `reason` field. When that rule rejects a call, the agent sees `denied by rule: <reason>` and the reason is recorded in audit.

```json
{
  "tool": "github.delete_*",
  "verdict": "deny",
  "reason": "repository deletion is disabled"
}
```

Rules can optionally constrain on argument values using the `args` field. All patterns must match (AND semantics) for a rule to fire.

```json
{
  "rules": [
    {
      "tool": "local-git.push",
      "verdict": "allow",
      "args": [{ "path": "remote", "match": "origin" }]
    },
    {
      "tool": "local-git.push",
      "verdict": "deny",
      "args": [{ "path": "commit.message", "match": { "regex": "^chore:" } }]
    },
    { "tool": "local-git.push", "verdict": "require-approval" }
  ]
}
```

`path` is a dot-separated field path (e.g. `remote`, `commit.message`, `command.0`). `match` is either a bare string for exact matching or `{"regex": "<RE2 pattern>"}` for regex matching. If a path cannot be resolved (missing key, wrong type, out-of-range index), the rule does not match and evaluation continues.

Note: regexes are not auto-anchored — use `^...$` for full-match semantics.

### Tool patches

Tool patches are optional load-time transforms for discovered tools. They match the broker-prefixed tool name, such as `github.search_code`, using the same `filepath.Match` glob syntax as rules. Patches are evaluated top-to-bottom and only the first matching patch applies.

```json
{
  "tool_patches": [
    {
      "tool": "github.search_*",
      "annotations": {
        "title": "GitHub search",
        "readOnlyHint": true,
        "destructiveHint": false
      }
    },
    { "tool": "github.delete_*", "disabled": true }
  ]
}
```

`annotations` merges field-by-field with upstream MCP tool annotations: fields present in the patch override upstream values, omitted fields are preserved, and missing upstream annotations are created. Supported fields are `title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, and `openWorldHint`.

`disabled: true` removes matching tools from the broker registry. Disabled tools do not appear in MCP `tools/list` or the dashboard and cannot be called through the broker.

## Authentication

First initialization creates distinct 64-character credentials at `~/.config/mcp-broker/agent-token` and `~/.config/mcp-broker/admin-token` (mode `0600`). If only a valid legacy `auth-token` exists, its normalized value becomes `agent-token`, a fresh distinct admin value is created, and the legacy path is retired after both canonical files are durable. The legacy path is migration input only, never a runtime fallback.

**MCP clients** send `Authorization: Bearer <agent-token>` to exact `/mcp`. **Dashboard clients** use only `admin-token`: an interactive startup may print/open `http://localhost:8200/dashboard/?token=<admin-token>`, which exchanges the query value for the dashboard-scoped cookie and redirects to a clean current URL. Non-interactive startup never prints, logs, or opens that URL. The redirect removes the query from the current URL; it does not promise removal from browser history.

```bash
mcp-broker token show agent   # Print only the selected raw credential
mcp-broker token show admin
mcp-broker token rotate agent # Rotate only agent-token; raw value is not printed
mcp-broker token rotate admin # Rotate only admin-token; raw value is not printed
```

Agent rotation is a coordinated cutover, not zero-downtime revocation: rotate the host file, refresh `copy_paths`/re-provision while avoiding new agent starts, send `SIGHUP` promptly, then reconnect clients holding the old value. After activation, old agent credentials fail on new MCP HTTP requests; existing MCP streaming responses may drain. For admin rotation, rotate, send `SIGHUP`, then reopen the dashboard; old credentials and cookies fail on new dashboard requests, while an already-open SSE stream may continue. The untouched role remains valid.

Downgrading to a one-token binary re-merges agent and dashboard authority. A deliberate rollback requires stopping or isolating the broker, reconstructing legacy shared-token state, and treating every sandbox holder as dashboard-authorized until re-upgrade and rotation.

Background clients that must not wait for human approval can add `Mcp-Broker-Approval-Mode: reject` to individual tool calls, or set it as a default HTTP header for that client.

## How the sandbox consumes it

The sandbox needs only `~/.config/mcp-broker/agent-token`; never copy, mount, export, or embed `admin-token`. Update existing external `copy_paths` from `auth-token` before the next `sb provision`; already-running guests keep working because migration preserves the old value as the agent credential.

### With sandbox-manager

```json
{
  "copy_paths": ["~/.config/mcp-broker/agent-token"],
  "scripts": [
    "/path/to/agent-tools/mcp-broker/examples/provision/configure-mcp-broker.sh"
  ]
}
```

The provisioning script reads that file at shell startup and exports `MCP_BROKER_URL=http://host.lima.internal:8200/mcp` plus `MCP_BROKER_TOKEN` in a marker-fenced `~/.bashrc` block. Wire those into the agent's MCP config.

### Without sandbox-manager

Copy only `agent-token` into the sandbox, then run [`examples/provision/configure-mcp-broker.sh`](examples/provision/configure-mcp-broker.sh). The script targets bash; adapt the rc-file write for other shells.

## Run as a launchd agent (macOS)

To keep the broker running in the background whenever you're logged in, install it as a per-user LaunchAgent. See [docs/launchd.md](docs/launchd.md) for setup (including how to keep secrets out of the plist), install, verify, and manage steps.

## CLI

```
mcp-broker serve              # Start the broker
mcp-broker serve -v           # Enable debug logging
mcp-broker serve --log-level debug  # Same, with explicit level
mcp-broker token show <agent|admin>    # Print only one selected credential
mcp-broker token rotate <agent|admin>  # Rotate only one role; activate with SIGHUP
mcp-broker grant mint --name NAME --ttl 1h --rules-file rules.json
mcp-broker grant list          # List retained active/expired/revoked grants
mcp-broker grant revoke ID     # Revoke by grant ID or fingerprint
mcp-broker logout <server>     # Clear a backend's cached OAuth credentials
mcp-broker config path        # Print config file path
mcp-broker config refresh     # Backfill new defaults into config and ensure rules file exists
mcp-broker config edit        # Open config in $EDITOR
mcp-broker rules path         # Print rules file path
mcp-broker rules refresh      # Write rules file in canonical form
mcp-broker rules edit         # Open rules file in $EDITOR
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

### Request flow

```mermaid
flowchart TD
    Agent["MCP Client"] -->|"HTTP + Bearer token"| MCP["MCP Server"]
    CLI["Broker CLI"] -->|"HTTP + Bearer token"| MCP

    MCP --> Grants["Grant Overlay (optional)"]
    Grants --> Rules["Rules Engine"]

    Rules -->|allow| Proxy["Proxy"]
    Rules -->|deny| Error["Return Error"]
    Rules -->|require-approval| Approver["Approver"]

    Approver -->|approved| Proxy
    Approver -->|denied| Error

    Proxy -->|stdio| Git["Local MCP Server"]
    Proxy -->|HTTP / SSE| Remote["Remote MCP Server"]

    Git --> Response["Return Response"]
    Remote --> Response

    Error --> Audit["Auditor"]
    Response --> Audit

    style MCP fill:#4a90d9,color:#fff
    style Rules fill:#4a90d9,color:#fff
    style Approver fill:#4a90d9,color:#fff
    style Proxy fill:#4a90d9,color:#fff
    style Agent fill:#95a5a6,color:#fff
    style CLI fill:#95a5a6,color:#fff
    style Git fill:#9b59b6,color:#fff
    style Remote fill:#9b59b6,color:#fff
    style Audit fill:#4a90d9,color:#fff
    style Response fill:#7ed321,color:#fff
    style Error fill:#d0021b,color:#fff
```

### Package layout

```
cmd/mcp-broker/         CLI entry point (Cobra)
internal/
  config/               JSON config load/save/refresh
  rules/                Glob-based rule engine
  audit/                SQLite audit logger
  server/               Backend MCP client (stdio, HTTP, SSE, OAuth transports)
  dashboard/            Web UI with approval flow, SSE, audit viewer
  telegram/             Telegram Bot API polling approver (opt-in)
  hooks/                Bounded asynchronous approval command observers
  broker/               Core orchestrator (rules → approval → proxy → audit)
```
