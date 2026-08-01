# Configuration

`~/.config/egress-broker/config.json`. Missing fields are backfilled with
defaults on load; `egress-broker config refresh` writes them back explicitly.

```json
{
  "proxy":     { "host": "127.0.0.1", "port": 8220 },
  "dashboard": { "host": "127.0.0.1", "port": 8221 },
  "rules_path": "~/.config/egress-broker/rules.json",
  "audit":     { "path": "~/.local/share/egress-broker/audit.db", "retention_days": 90 },
  "log":       { "level": "info" },
  "open_browser": true,
  "env_credentials": {}
}
```

| Field | Default | Notes |
| --- | --- | --- |
| `proxy` | `127.0.0.1:8220` | **Loopback only**, enforced at startup. |
| `dashboard` | `127.0.0.1:8221` | Must differ from `proxy`. |
| `rules_path` | alongside `config.json` | |
| `audit.path` | `~/.local/share/egress-broker/audit.db` | Created `0600`, with its WAL sidecars. |
| `audit.retention_days` | `90` | `0` disables pruning. |
| `log.level` | `info` | `debug`, `info`, `warn`, `error`. |
| `env_credentials` | `{}` | See below. |

## Loopback only

Both listeners are rejected at startup unless they bind loopback. The network
boundary is load-bearing; the bearer token is defence in depth. Sandboxes reach
the host through Lima's forwarding of `host.lima.internal` to host loopback, so
a loopback bind is sufficient — and a non-loopback bind would expose a
credential-injecting proxy to the local network, which no token makes
acceptable.

The two listeners may not share a port: the dashboard would then be reachable
*through* the proxy.

## `env_credentials`

```json
"env_credentials": {
  "gh_bot": { "var": "GH_TOKEN", "hosts": ["api.github.com"] }
}
```

Both fields are required. A credential with no bound hosts is rejected at load.

Prefer the keychain. This exists for headless Linux, CI, and the test suite.
`serve` logs a warning naming every credential sourced this way.

## Paths

| Path | Holds |
| --- | --- |
| `~/.config/egress-broker/config.json` | This file. `0600`. |
| `~/.config/egress-broker/rules.json` | Policy. `0600`. |
| `~/.config/egress-broker/auth-token` | Shared bearer token. `0600`. |
| `~/.local/share/egress-broker/audit.db` | Audit log. `0600`. |
| `~/.local/share/egress-broker/ca.key` | CA private key. `0600`. Never leaves the host. |
| `~/.local/share/egress-broker/ca.pem` | CA certificate. `0644`. Shipped into sandboxes. |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` are honoured.

## Environment

| Variable | Purpose |
| --- | --- |
| `EGRESS_BROKER_TEST_ALLOW_ADDRS` | **Tests only.** Comma-separated exact `host:port` targets that skip the address guard, so a test can reach a mock upstream on loopback. Never set this in production; `serve` logs a warning naming every exemption. |
