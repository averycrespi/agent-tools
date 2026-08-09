# http-broker

MITM HTTP/HTTPS forward proxy that injects credentials for sandboxed AI agents,
so the agent never holds a secret.

The network-layer sibling to `mcp-broker`: same premise, applied to raw HTTP
instead of MCP tool calls.

> **This is not a containment boundary.** Enforcement is cooperative — it rests
> on the sandbox honouring `HTTP_PROXY`/`HTTPS_PROXY`. An agent that unsets
> them, uses `curl --noproxy`, or opens a raw socket bypasses this tool
> entirely. It makes credential handling safe for cooperating traffic and makes
> that traffic auditable. See [docs/security-model.md](docs/security-model.md).

## Install

```bash
make install     # go install ./cmd/http-broker
```

## Quick start

```bash
# 1. Start the proxy. First run generates config, rules, a CA, and distinct
#    agent/admin credentials. An interactive terminal may open the dashboard;
#    pass --no-open to skip it.
http-broker serve

# 2. Store a credential, bound to the hosts it may be sent to.
printf %s "$GITHUB_TOKEN" | http-broker credential set gh_bot --host api.github.com

# 3. Write a rule that injects it.
$EDITOR ~/.config/http-broker/rules.json
http-broker rules check

# 4. Reload without restarting.
kill -HUP $(pgrep -f 'http-broker serve')
```

Point a sandbox at it with
[`examples/provision/configure-http-broker.sh`](examples/provision/configure-http-broker.sh).

### Required `copy_paths`

The provisioning script does **not** fetch anything over the network.
`sandbox-manager` must ship both files in:

```json
"copy_paths": [
  "~/.config/http-broker/agent-token",
  "~/.local/share/http-broker/ca.pem"
]
```

Only `agent-token` enters sandboxes; `admin-token` stays on the host. On upgrade, a valid legacy `auth-token` is preserved as the canonical agent value, a distinct admin value is generated, and the legacy path is retired. Update external `copy_paths` before the next provision; already-running guests retain the preserved value.

## Rules

```json
{
  "fallthrough": "tunnel",
  "rules": [
    {
      "name": "github-issues",
      "host": "api.github.com",
      "path": "/repos/*/*/issues",
      "method": "POST",
      "mode": "intercept",
      "inject": {
        "set": {
          "Authorization": "Bearer ${cred.gh_bot}",
          "X-GitHub-Api-Version": "2022-11-28"
        },
        "remove": ["X-Agent-Hint"]
      }
    },
    {
      "name": "internal-api",
      "host": "api.internal.example.com",
      "mode": "intercept",
      "allow_private": true,
      "min_tls_version": "1.2",
      "inject": { "set": { "Authorization": "Bearer ${cred.internal}" } }
    },
    { "name": "pinned-sdk", "host": "*.pinned-sdk.com", "mode": "tunnel" },
    { "name": "no-ads", "host": "*.doubleclick.net", "mode": "deny" }
  ]
}
```

Three modes: `intercept` terminates TLS and may inject; `tunnel` relays opaque
bytes; `deny` refuses. See [docs/rules.md](docs/rules.md) for the full matching
procedure.

**Deny beats intercept regardless of rule order**, so reordering the file for
readability cannot change whether a credential reaches a path.

Dials into private address space are refused unless a host-only rule sets
`"allow_private": true`, which is how an internal service is reached. It relaxes
that one address class for the hosts its glob matches — loopback, link-local and
cloud-metadata addresses stay refused — so keep the glob narrow.

Upstream TLS is 1.3 by default. A host that cannot negotiate it — an ALB on an
older security policy, for instance — needs `"min_tls_version": "1.2"` on its
rule. Certificate verification is on at every floor.

## Credentials

Every credential carries **bound hosts**, whatever its source. Injecting into a
request whose host falls outside them is refused with 403 and audited.

```bash
printf %s "$TOKEN" | http-broker credential set gh_bot --host api.github.com
http-broker credential list
http-broker credential rebind gh_bot --host api.github.com --host uploads.github.com
http-broker credential rm gh_bot
```

The rule's `host` glob decides whether a rule fires. The credential's bound
globs decide whether that credential may go to that host. Both must pass —
without the second check a single rule-authoring slip sends a real token
wherever the rule now matches.

No subcommand prints a value. `list` and `get` show a name, its source, its
bound hosts, and the byte count of the stored value. A rebind reaches a running
proxy within 30s, or immediately with
`kill -HUP $(pgrep -f 'http-broker serve')`.

The keychain cannot be enumerated, so stored names are recorded in
`~/.local/share/http-broker/credentials.json`. That file holds names only, and
deleting it loses no secret — `http-broker credential get <name>` re-registers
one.

### Keychain versus `env_credentials`

**Use the keychain.** It is the default and the reason the launchd plist can
carry `PATH` and nothing else.

`env_credentials` in `config.json` reads a value from a named environment
variable instead:

```json
"env_credentials": {
  "gh_bot": { "var": "GH_TOKEN", "hosts": ["api.github.com"] }
}
```

Use it only for headless Linux and CI, where there is no Secret Service daemon
for `go-keyring` to reach, and for the test suite, which must never touch a
real keychain. Entries carry the same host binding and go through the same
check — but the value sits in the process environment, which is what the
keychain design is avoiding. `serve` logs a warning naming every credential
sourced this way.

**An unreachable keychain is a hard error, never a file fallback.** A plaintext
secret on disk is the outcome the keychain exists to avoid.

## Operations

### Recovery: edit rules, do not kill the process

Proxy variables are baked into every sandbox's shell. Killing `serve` points
them all at a dead socket and causes total network loss — worse than whatever
prompted the kill.

The kill switch is an all-tunnel `rules.json` plus `SIGHUP`:

```json
{ "fallthrough": "tunnel", "rules": [] }
```

```bash
kill -HUP $(pgrep -f 'http-broker serve')
```

Injection stops for every new connection; the network keeps working. A failed
reload leaves the previous ruleset serving and logs an error, so check the log
after every reload.

A connection already established keeps the ruleset its CONNECT was decided
under, so that the connect-time decision and the per-request decisions on one
connection can never disagree. Existing intercepted connections therefore drain
under the old rules — bounded by the idle timeout, five minutes by default. If
you need injection to stop for traffic already in flight, restart instead and
accept the outage.

### The wedged-but-listening failure

`KeepAlive` restarts a **crashed** process. It does not notice one still
listening but no longer serving — and that failure is worse here, because every
sandbox's network depends on this socket. A wedged proxy also answers no
signals, so the rules-edit kill switch cannot help.

Probe from outside and restart on failure:

```bash
curl -fsS --max-time 5 http://127.0.0.1:8221/healthz >/dev/null || <restart>
```

`/healthz` needs no token, precisely so a probe can be configured without
handling one. See [docs/launchd.md](docs/launchd.md).

### CA rotation

```bash
http-broker ca rotate --yes
```

**There is no overlap window.** Every provisioned sandbox stops trusting the
proxy the moment rotation completes, and TLS interception fails there until
provisioning is re-run in each one. Re-provision every sandbox, then `SIGHUP`.

Rotate when the key may have leaked, not as routine maintenance.

### Role credential rotation

```bash
http-broker token rotate agent
http-broker token rotate admin
```

Agent rotation is a coordinated cutover, not zero-downtime revocation: rotate the host file, refresh `copy_paths`/re-provision while avoiding new client starts, send `SIGHUP` promptly, then reconnect clients with the old value. New CONNECT and absolute-form requests reject the old credential after activation; traffic inside established tunnel or MITM CONNECT connections continues.

For admin rotation, rotate, send `SIGHUP`, then reopen the dashboard. Old Bearer credentials and cookies fail on new dashboard requests, while an already-open SSE stream may continue. The untouched role remains valid. `SIGHUP` checks both role files independently even when config, rules, or CA reload fails; an invalid candidate retains only that role's previous in-memory value.

Downgrading to a one-token binary re-merges proxy and dashboard authority. A deliberate rollback requires stopping or isolating the broker, reconstructing shared-token state, and treating every sandbox holder as dashboard-authorized until re-upgrade and rotation.

## Known limitations

### Certificate-pinning clients fail

A client that pins a certificate rejects our leaf by design. The signature is a
TLS handshake failure at the client, with a proxy log line reading:

```
client TLS handshake failed (a certificate-pinning client needs a mode:"tunnel" rule)
```

The fix is a `tunnel` rule for that host. The traffic is then relayed
un-inspected, which is the trade.

### WebSocket and other `Upgrade` traffic

`Upgrade` is stripped as a hop-by-hop header, so a WebSocket handshake through
an `intercept` rule fails — typically as an HTTP 200 where the client expected
101, or a "bad handshake" error. Route such hosts with `mode: "tunnel"`.

### The real MCP client is not Go

`NO_PROXY` is verified end to end against a Go client. The agent's own MCP
client is Node's undici, which honours proxy variables only partially and
version-dependently. **After first provisioning, manually confirm that
`mcp-broker` and `local-gomod-proxy` are still reachable from inside the
sandbox** — that check is not covered by the test suite.

### Headless Linux has no keychain

`go-keyring` needs a Secret Service daemon. The intended host is macOS; use
`env_credentials` elsewhere.

### `fallthrough: "deny"` breaks a fresh sandbox

Until rules exist, nothing is reachable. The generated default is `tunnel` for
that reason. Tightening to `deny` with an explicit allowlist is the intended
end state.

## Commands

| Command                                 | Purpose                                        |
| --------------------------------------- | ---------------------------------------------- |
| `serve [--no-open]`                     | Run the proxy and dashboard in the foreground. |
| `config path\|show\|refresh`            | Inspect and backfill `config.json`.            |
| `rules path\|show\|check\|refresh`      | Inspect and validate `rules.json`.             |
| `credential set\|list\|get\|rebind\|rm` | Manage credentials and their host bindings.    |
| `token show <agent\|admin>`             | Print one selected role credential.            |
| `token rotate <agent\|admin>`           | Rotate one role and print activation guidance. |
| `token proxy-credential`                | Print an agent-derived proxy credential.       |
| `ca export\|rotate\|path`               | Manage the interception CA.                    |

See [docs/cli.md](docs/cli.md).

## Documentation

- [DESIGN.md](DESIGN.md) — what this should be and why.
- [docs/security-model.md](docs/security-model.md) — trust boundaries,
  guarantees, out-of-scope, compromise response.
- [docs/rules.md](docs/rules.md) — the matching procedure.
- [docs/config.md](docs/config.md) — `config.json` reference.
- [docs/cli.md](docs/cli.md) — command reference.
- [docs/dashboard.md](docs/dashboard.md) — routes and what they expose.
- [docs/launchd.md](docs/launchd.md) — supervision on macOS.
