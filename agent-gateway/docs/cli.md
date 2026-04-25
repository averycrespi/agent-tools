# CLI

`agent-gateway` is a single Cobra-based binary. This document covers every command.

## Global flags

| Flag              | Description                                                                          |
| ----------------- | ------------------------------------------------------------------------------------ |
| `--config <path>` | Override the config file path. Default: `$XDG_CONFIG_HOME/agent-gateway/config.hcl`. |

## CLI / daemon coordination

State-mutating commands write to SQLite (or to the filesystem, for rules and admin tokens) and then signal the running daemon via `SIGHUP`. If no daemon is running, the write still succeeds and the daemon picks up the new state on next start. Before signalling, the CLI verifies the PID's `comm` name to guard against PID reuse.

## Confirmation prompts

Destructive commands prompt for `[y/N]` confirmation before proceeding:

- `agent rotate <name>`
- `agent rm <name>`
- `secret update <name>`
- `secret rm <name>`
- `master-key rotate`
- `ca rotate`
- `admin-token rotate`

Each of these accepts a `--force` flag to skip the prompt. When stdin is not a TTY (scripted use), the prompt cannot be shown and the command refuses unless `--force` is passed — scripts must opt into destructive actions explicitly.

`secret update` reads the new value from stdin, so its confirmation prompt reads from `/dev/tty` instead of stdin. If `/dev/tty` is unavailable (headless CI without a controlling terminal), pass `--force`.

## `serve`

Start the proxy and dashboard.

```bash
agent-gateway serve
agent-gateway serve --headless
```

| Flag         | Description                                                                |
| ------------ | -------------------------------------------------------------------------- |
| `--headless` | Suppress the browser launch on startup. Useful on CI and headless servers. |

Binds `:8220` (proxy) and `:8221` (dashboard) per config. On every startup, prints the dashboard URL with its admin token to stdout and opens it in a browser when `dashboard.open_browser = true` (the default) and `--headless` is not set. The admin token is generated on first run and persisted at `$XDG_CONFIG_HOME/agent-gateway/admin-token`.

Signals:

- `SIGHUP` — partial reload (see table below). In-flight requests finish on the pre-reload snapshot; no requests are dropped.
- `SIGTERM` / `SIGINT` — graceful shutdown with 30s in-flight grace.

### SIGHUP reload semantics

The daemon reloads components in this order: agents → rules → injector cache → admin token → CA.

| Setting / component                        | Reloaded on SIGHUP? | Notes                                                                                                                                                                  |
| ------------------------------------------ | ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Agent registry (`agents` table)            | Yes                 | Re-read from SQLite; new token hashes are live before rule reload.                                                                                                     |
| Rules files (`rules.d/*.hcl`)              | Yes                 | Previous ruleset stays live if the new files fail to parse.                                                                                                            |
| Injector secret-value cache                | Yes (cleared)       | Forces re-fetch from SQLite so `secret update <name>` propagates immediately. The active TTL (`secrets.cache_ttl`) is unchanged — only the cache contents are dropped. |
| Admin token file                           | Yes                 | Re-read from `$XDG_CONFIG_HOME/agent-gateway/admin-token`.                                                                                                             |
| Root CA key/cert (`ca.key` / `ca.pem`)     | Yes                 | Re-read from disk; leaf-cert cache is cleared.                                                                                                                         |
| `config.hcl` — any field                   | **No**              | Requires daemon restart.                                                                                                                                               |
| `proxy.listen`, `dashboard.listen`         | No                  | Would require re-binding the listener socket.                                                                                                                          |
| `timeouts.*`                               | No                  | Baked into server construction at startup.                                                                                                                             |
| `proxy_behavior.no_intercept_hosts`        | No                  | Baked into proxy construction at startup.                                                                                                                              |
| `proxy_behavior.max_body_buffer`           | No                  | Baked into proxy construction at startup.                                                                                                                              |
| `approval.timeout`, `approval.max_pending` | No                  | Baked into approval broker at startup.                                                                                                                                 |
| `secrets.cache_ttl`                        | No                  | Active TTL is set at startup; only the cache contents are cleared.                                                                                                     |
| `audit.retention_days`                     | No                  | Pruner uses the startup value.                                                                                                                                         |

## `agent`

Manage registered agents. Every agent has a name, a token (`agw_…`, printed once at `add`), and a 12-char token prefix (`agw_` + 8 body chars) used for disambiguation in listings.

### `agent add <name>`

Register a new agent and print its token.

```bash
agent-gateway agent add claude-review
agent-gateway agent add claude-review --desc "Code review bot"
```

| Flag            | Description                 |
| --------------- | --------------------------- |
| `--desc <text>` | Human-readable description. |

Prints the raw token once, followed by a ready-to-paste `HTTPS_PROXY` / `HTTP_PROXY` block. The token is not recoverable after this — if lost, rotate.

### `agent list`

List all agents with metadata (no tokens, only prefixes).

| Flag                   | Description                                       |
| ---------------------- | ------------------------------------------------- |
| `-o`, `--output <fmt>` | `text` (default) or `json`. See JSON shape below. |

#### `agent list -o json` shape

Token hashes, salts, and descriptions are deliberately omitted.

```json
{
  "agents": [
    {
      "name": "claude-review",
      "prefix": "agw_a1b2c3d4",
      "created_at": "2026-04-24T10:00:00Z",
      "last_seen_at": "2026-04-24T11:23:45Z"
    }
  ]
}
```

`last_seen_at` is `null` for agents that have never made a request.

### `agent rotate <name>`

Mint a new token for an existing agent. The previous token is invalidated **immediately** — there is no grace window. Prints the new token and proxy URL block, same format as `agent add`.

| Flag      | Description               |
| --------- | ------------------------- |
| `--force` | Skip confirmation prompt. |

### `agent rm <name>`

Remove an agent. Transactionally cascades to agent-scoped secrets (`scope = 'agent:<name>'`). Audit rows referencing the agent retain the agent name as-is (no FK on `requests.agent`, so values are left intact — history survives deletion for forensics).

| Flag      | Description               |
| --------- | ------------------------- |
| `--force` | Skip confirmation prompt. |

## `secret`

Manage encrypted secrets. Values are stored AES-256-GCM-encrypted in SQLite; the master key lives in the OS keychain at `master-key-<id>` (with a `master-key-<id>` file fallback). The active id is tracked in the SQLite `meta` table; rotation increments it. Values are never logged, never surfaced on the dashboard, and never reflected through HTTP.

### `secret add <name>`

Store a new secret. The value is read from **stdin** (piped input only — refuses when stdin is a TTY). Every secret must be bound to at least one host glob via `--host` (repeatable). Bindings gate which rules may inject this secret: a `${secrets.X}` reference expanded into a request whose target host does not match any of the secret's bound hosts produces a hard `403 Forbidden` and an audit row with `error='secret_host_scope_violation'`.

```bash
echo -n "ghp_abc123…" | agent-gateway secret add gh_bot --host "*.github.com"
agent-gateway secret add gh_bot --host api.github.com --host "*.githubusercontent.com" --agent claude-review --desc "GitHub bot token"
agent-gateway secret add unrestricted --host "**"    # explicit all-hosts opt-in
```

| Flag             | Description                                                                                                                                                                                                                               |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--host <glob>`  | Host glob the secret may be injected into. Repeatable; at least one is **required**. Same glob semantics as rule `match.host`. Use `"**"` for an explicit all-hosts opt-in. Wildcard-only patterns like `*`, `*.*`, or `..` are rejected. |
| `--agent <name>` | Scope to a specific agent. Omit for global scope.                                                                                                                                                                                         |
| `--desc <text>`  | Human-readable description.                                                                                                                                                                                                               |

A warning is printed if an agent-scoped add would shadow an existing global secret of the same name. Scope resolution is most-specific-wins: `agent:<name>` beats `global` for the same `<name>`.

### `secret bind <name>`

Add one or more host globs to an existing secret's `allowed_hosts` list. Idempotent — re-binding an existing pattern is a no-op.

```bash
agent-gateway secret bind gh_bot --host "*.github.com"
agent-gateway secret bind gh_bot --host api.github.com --host "*.githubusercontent.com"
```

| Flag             | Description                                             |
| ---------------- | ------------------------------------------------------- |
| `--host <glob>`  | Host glob to add. Repeatable; at least one is required. |
| `--agent <name>` | Scope to the agent-scoped row for this secret name.     |

### `secret unbind <name>`

Remove one or more host globs from a secret's `allowed_hosts` list. **Errors** if the removal would leave the list empty — every secret must remain bound to at least one host. Rebind first, or use `secret rm` to delete the secret outright.

```bash
agent-gateway secret unbind gh_bot --host "*.github.com"
```

| Flag             | Description                                                |
| ---------------- | ---------------------------------------------------------- |
| `--host <glob>`  | Host glob to remove. Repeatable; at least one is required. |
| `--agent <name>` | Scope to the agent-scoped row for this secret name.        |

### `secret list`

List secrets (metadata only — no values, ever). The `HOSTS` column shows the comma-separated `allowed_hosts` list for each row.

| Flag                   | Description                                       |
| ---------------------- | ------------------------------------------------- |
| `-o`, `--output <fmt>` | `text` (default) or `json`. See JSON shape below. |

#### `secret list -o json` shape

Encrypted values, nonces, and descriptions are deliberately omitted.

```json
{
  "secrets": [
    {
      "name": "gh_bot",
      "scope": "global",
      "allowed_hosts": ["api.github.com", "*.githubusercontent.com"],
      "created_at": "2026-04-24T10:00:00Z",
      "rotated_at": "2026-04-24T10:00:00Z",
      "last_used_at": "2026-04-24T11:23:45Z"
    }
  ]
}
```

`scope` is `"global"` or `"agent:<name>"`. `last_used_at` is `null` until the first injection.

### `secret update <name>`

Update the value of an existing secret. Reads the new value from stdin. On update, the decrypted-secret LRU is invalidated so the next request uses the new value.

```bash
echo -n "new-value" | agent-gateway secret update gh_bot
agent-gateway secret update gh_bot --agent claude-review
```

| Flag             | Description                           |
| ---------------- | ------------------------------------- |
| `--agent <name>` | Scope the update to a specific agent. |
| `--force`        | Skip confirmation prompt.             |

### `secret rm <name>`

Delete a secret.

| Flag             | Description                                                 |
| ---------------- | ----------------------------------------------------------- |
| `--agent <name>` | Delete the agent-scoped row; omit to delete the global row. |
| `--force`        | Skip confirmation prompt.                                   |

## `master-key rotate`

Generate a new master key and re-encrypt every secret under it in a single SQLite transaction. The new key is only committed to storage after the re-encryption transaction succeeds — a crash mid-rotation leaves the old key authoritative.

| Flag      | Description               |
| --------- | ------------------------- |
| `--force` | Skip confirmation prompt. |

## `rules`

Validate and reload rule files. See [rules.md](./rules.md) for rule syntax.

### `rules check`

Validate rule files without running the daemon.

```bash
agent-gateway rules check
```

Reads rules from `$XDG_CONFIG_HOME/agent-gateway/rules.d/` and cross-references `${secrets.X}` template references against the live state DB. Exits non-zero on parse errors. Unknown secret references are reported as warnings on stdout but do not affect the exit code. If the state DB is unavailable (e.g. on a fresh install), a single "could not open state db" warning is written to stderr and every `${secrets.X}` reference becomes a warning — fail-open, so `rules check` never blocks on missing infrastructure.

### `rules reload` (deprecated)

Hidden alias for `agent-gateway reload`. Prints a deprecation notice on stderr but still performs the reload. Use the top-level `reload` command instead.

## `reload`

Signal the running daemon to reload all SIGHUP-mutable state: rules, agents, secrets cache, admin token, CA. Prints `reloaded` on success, errors with `no daemon running` if the PID file is absent.

```bash
agent-gateway reload
```

Invalid rule files leave the previous rule-set live — see [rules.md](./rules.md#reload). `config.hcl` is **not** re-parsed; if its sha256 differs from the value the daemon recorded at startup, `reload` prints a warning so you know a restart is needed. See [config.md](./config.md).

## `admin-token rotate`

Generate a new admin dashboard token. The new token is written to `$XDG_CONFIG_HOME/agent-gateway/admin-token` and printed to stdout. The running daemon is signalled to reload the token in memory.

```bash
agent-gateway admin-token rotate
```

| Flag      | Description               |
| --------- | ------------------------- |
| `--force` | Skip confirmation prompt. |

The dashboard cookie from the old token becomes invalid immediately. Re-authenticate by visiting `http://127.0.0.1:8221/dashboard/?token=<new-token>` or via the re-auth form on the unauthorized page.

## `ca`

Manage the local root CA. Leaf certificates for intercepted hosts are issued on demand (24-hour validity, cached in memory); the root CA is persisted to disk.

### `ca export`

Print the root CA certificate (PEM) to stdout. Primary path for distributing trust to sandboxes.

```bash
agent-gateway ca export > /tmp/agent-gateway-ca.pem
```

The gateway also serves the CA at `http://127.0.0.1:8221/ca.pem` (unauthenticated by design — public-key material).

### `ca rotate`

Generate a fresh root CA, replacing the one on disk. **Disruptive** — every sandbox must re-trust the new CA. The running daemon is signalled so new TLS sessions use the new root.

| Flag      | Description               |
| --------- | ------------------------- |
| `--force` | Skip confirmation prompt. |

## `config`

Manage `config.hcl`.

### `config path`

Print the active config file path.

### `config edit`

Open the config file in `$EDITOR` (falling back to `vi`). If the file does not exist, it is created with current defaults before the editor opens.

### `config refresh`

Rewrite the config file, preserving existing overrides and back-filling any new default keys. Use after a version upgrade that adds new config options.

## `X-Agent-Gateway-Reason` codes

Every 4xx / 5xx the proxy synthesises carries an `X-Agent-Gateway-Reason` response header naming the specific check that failed. The string is stable — script against it.

| Code                    | HTTP status | Cause                                                                                                                     |
| ----------------------- | ----------- | ------------------------------------------------------------------------------------------------------------------------- |
| `body-matcher-bypassed` | 403         | Request body exceeded `proxy_behavior.max_body_buffer` while a rule had a body matcher; failed closed.                    |
| `body-read-error`       | 403         | I/O error while buffering the request body for a body-matcher rule.                                                       |
| `rule-deny`             | 403         | A rule matched and its verdict is `deny`.                                                                                 |
| `unknown-verdict`       | 403         | A rule matched with a verdict the proxy does not recognise (version skew). Fail-closed by design.                         |
| `secret-invalid`        | 403         | Secret value contains characters that cannot be safely injected into a header.                                            |
| `secret-unresolved`     | 403         | A `${secrets.X}` reference could not be resolved (secret missing, or scope mismatch). Add the secret with `secret add`.   |
| `forbidden-host`        | 403         | The injected secret is not bound to the request's host. Bind with `secret bind`.                                          |
| `approval-denied`       | 403         | A `require-approval` rule matched and the dashboard operator clicked deny.                                                |
| `queue-full`            | 503         | Approval-broker queue is at `approval.max_pending` (or `max_pending_per_agent`). Response also carries `Retry-After: 30`. |
| `no-approval-broker`    | 504         | A `require-approval` rule matched but no approval broker is configured.                                                   |
| `approval-timeout`      | 504         | A `require-approval` rule matched and `approval.timeout` elapsed without a decision.                                      |
| `approval-broker-error` | 502         | Approval broker returned an unexpected error.                                                                             |
| `build-request-error`   | 500         | Internal error constructing the upstream request after rule evaluation. Indicates a bug — check the daemon log.           |
| `upstream-error`        | 502         | Upstream dial / TLS handshake / connection error.                                                                         |

The header pairs with the audit row's `Error` column, where applicable, for after-the-fact correlation.

## Exit codes

- `0` — success, or "no daemon running" on commands that signal the daemon.
- Non-zero — parse errors, unrecoverable I/O failures, missing records (`agent rotate` / `rm`, `secret update` / `rm` against a missing name).
