# Configuration reference

`config.hcl` is the daemon's static configuration. It lives at
`$XDG_CONFIG_HOME/agent-gateway/config.hcl` (`0o600`, parent dir `0o700`)
and is also printed by `agent-gateway config path`.

> **All fields in `config.hcl` are restart-only.** SIGHUP (`agent-gateway
reload`) does _not_ re-parse this file. To apply an edit, send SIGTERM
> to the running daemon and re-invoke `agent-gateway serve`. The daemon
> records a `sha256` of the file in the SQLite `meta` table at startup;
> `agent-gateway reload` and `agent-gateway config edit` warn when the
> on-disk file differs from that hash.

For everything mutable at runtime — rules, agents, secrets, the admin
token, the CA — see `docs/cli.md` and `docs/rules.md`. Those surfaces
_are_ picked up by SIGHUP.

The startup banner (`agent-gateway serve`) prints the resolved paths the
daemon is using, including the config file, state DB, CA cert, and PID
file. Use it to confirm where the file you're editing actually lives.

---

## `proxy` block

Where the proxy listens for sandbox traffic.

| Field    | Type   | Default          | Description                                                               |
| -------- | ------ | ---------------- | ------------------------------------------------------------------------- |
| `listen` | string | `127.0.0.1:8220` | Address for the HTTP CONNECT + plain-HTTP proxy listener (loopback only). |

## `dashboard` block

Where the dashboard / admin API listens, and whether the daemon opens a
browser on startup.

| Field          | Type   | Default          | Description                                                                 |
| -------------- | ------ | ---------------- | --------------------------------------------------------------------------- |
| `listen`       | string | `127.0.0.1:8221` | Address for the dashboard SPA, `/ca.pem`, and SSE feed (loopback only).     |
| `open_browser` | bool   | `true`           | If `true`, opens the dashboard URL in the default browser on `serve` start. |

## `rules` block

Where rule files live.

| Field | Type   | Default                           | Description                                            |
| ----- | ------ | --------------------------------- | ------------------------------------------------------ |
| `dir` | string | `~/.config/agent-gateway/rules.d` | Directory globbed for `*.hcl` rule files at load time. |

## `secrets` block

Tunes the in-memory decrypted-secret LRU.

| Field       | Type     | Default | Description                                                                                                                                  |
| ----------- | -------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `cache_ttl` | duration | `60s`   | How long a decrypted secret value is kept in memory after first use. SIGHUP also clears the cache for prompt propagation of `secret update`. |

## `audit` block

Audit-row retention and prune scheduling.

| Field            | Type   | Default | Description                                                                               |
| ---------------- | ------ | ------- | ----------------------------------------------------------------------------------------- |
| `retention_days` | int    | `90`    | Audit rows older than this are deleted on the daily prune.                                |
| `prune_at`       | string | `04:00` | Local-time `HH:MM` for the daily prune. Set to a low-traffic window for the gateway host. |

## `approval` block

Caps for the in-memory approval broker.

| Field                   | Type     | Default | Description                                                                                               |
| ----------------------- | -------- | ------- | --------------------------------------------------------------------------------------------------------- |
| `timeout`               | duration | `5m`    | How long a `require_approval` rule waits for a dashboard decision before synthesising 504.                |
| `max_pending`           | int      | `50`    | Total parked approval requests across all agents. Beyond this, the proxy returns 503 + `Retry-After: 30`. |
| `max_pending_per_agent` | int      | `10`    | Per-agent cap. Counts toward the same 503 response above when exceeded.                                   |

## `proxy_behavior` block

Knobs that change how the proxy treats specific traffic.

| Field                    | Type            | Default | Description                                                                                                                                                                                                                       |
| ------------------------ | --------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `no_intercept_hosts`     | list of strings | `[]`    | Glob patterns whose CONNECT targets are tunnelled raw (no MITM, no rule eval). Public-suffix patterns are rejected at load.                                                                                                       |
| `max_body_buffer`        | size string     | `1MiB`  | Cap on bytes buffered per request for body-matcher rules. Requests with bodies above this fail closed with 403 + `X-Agent-Gateway-Reason: body-matcher-bypassed`. Raise only if a legitimate rule needs to inspect a larger body. |
| `allow_private_upstream` | bool            | `false` | Allow upstream dials to RFC 1918 / loopback. Cloud IMDS (`169.254.169.254`, `fd00:ec2::254`) is _always_ blocked regardless of this setting.                                                                                      |

## `timeouts` block

Per-phase deadlines. Use durations like `"30s"`, `"5m"`. `0s` means **no deadline**.

| Field                      | Type     | Default | Description                                                                                                         |
| -------------------------- | -------- | ------- | ------------------------------------------------------------------------------------------------------------------- |
| `connect_read_header`      | duration | `10s`   | Deadline for reading the CONNECT request line + headers from the sandbox.                                           |
| `mitm_handshake`           | duration | `10s`   | Deadline for the inner TLS handshake with the sandbox after CONNECT is accepted.                                    |
| `idle_keepalive`           | duration | `120s`  | Idle timeout on the sandbox-facing keep-alive connection.                                                           |
| `upstream_dial`            | duration | `10s`   | Deadline for opening the TCP connection to upstream.                                                                |
| `upstream_tls`             | duration | `10s`   | Deadline for completing the upstream TLS handshake.                                                                 |
| `upstream_response_header` | duration | `30s`   | Deadline from request-write to first response byte from upstream.                                                   |
| `upstream_idle_keepalive`  | duration | `90s`   | Idle timeout on the upstream keep-alive connection.                                                                 |
| `body_buffer_read`         | duration | `30s`   | Per-request deadline for buffering a request body for a body-matcher rule. Independent of overall request lifetime. |

## `log` block

Daemon log handler.

| Field    | Type   | Default | Description                                                                          |
| -------- | ------ | ------- | ------------------------------------------------------------------------------------ |
| `level`  | string | `info`  | One of `debug`, `info`, `warn`, `error`. Controls the slog handler level.            |
| `format` | string | `text`  | One of `text`, `json`. `text` is human-readable; `json` is line-delimited slog JSON. |

---

## Applying changes

`config.hcl` is restart-only. To apply an edit:

```
agent-gateway config edit                       # opens $EDITOR; warns on changed fields
kill $(cat $XDG_CONFIG_HOME/agent-gateway/agent-gateway.pid)
agent-gateway serve                             # picks up the new file
```

`agent-gateway reload` does **not** re-parse `config.hcl`. It will,
however, warn if the file's sha256 differs from what was recorded at the
running daemon's startup — a useful "did I forget to restart?" signal.

For every other state surface (rules, agents, secrets, admin token, CA),
the relevant `agent-gateway *` mutation command is picked up immediately
via SIGHUP — see `docs/cli.md`.
