# egress-broker

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
make install     # go install ./cmd/egress-broker
```

## Quick start

```bash
# 1. Start the proxy. First run generates config, rules, a CA, and a token.
egress-broker serve

# 2. Store a credential, bound to the hosts it may be sent to.
printf %s "$GITHUB_TOKEN" | egress-broker credential set gh_bot --host api.github.com

# 3. Write a rule that injects it.
$EDITOR ~/.config/egress-broker/rules.json
egress-broker rules check

# 4. Reload without restarting.
kill -HUP $(pgrep -f 'egress-broker serve')
```

Point a sandbox at it with
[`examples/provision/configure-egress-broker.sh`](examples/provision/configure-egress-broker.sh).

### Required `copy_paths`

The provisioning script does **not** fetch anything over the network.
`sandbox-manager` must ship both files in:

```json
"copy_paths": [
  "~/.config/egress-broker/auth-token",
  "~/.local/share/egress-broker/ca.pem"
]
```

`sb provision` re-runs `copy_paths` before the scripts, so a rotated token or
CA is picked up on the next provision.

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

## Credentials

Every credential carries **bound hosts**, whatever its source. Injecting into a
request whose host falls outside them is refused with 403 and audited.

```bash
printf %s "$TOKEN" | egress-broker credential set gh_bot --host api.github.com
egress-broker credential list      # names, sources and bound hosts only
egress-broker credential rm gh_bot
```

The rule's `host` glob decides whether a rule fires. The credential's bound
globs decide whether that credential may go to that host. Both must pass —
without the second check a single rule-authoring slip sends a real token
wherever the rule now matches.

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
kill -HUP $(pgrep -f 'egress-broker serve')
```

Injection stops immediately; the network keeps working. A failed reload leaves
the previous ruleset serving and logs an error, so check the log after every
reload.

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
egress-broker ca rotate --yes
```

**There is no overlap window.** Every provisioned sandbox stops trusting the
proxy the moment rotation completes, and TLS interception fails there until
provisioning is re-run in each one. Re-provision every sandbox, then `SIGHUP`.

Rotate when the key may have leaked, not as routine maintenance.

### Token rotation

```bash
egress-broker token rotate
```

Re-provision every sandbox, then `SIGHUP`. Until a sandbox is re-provisioned,
its requests get 407.

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

| Command | Purpose |
| --- | --- |
| `serve` | Run the proxy and dashboard in the foreground. |
| `config path\|show\|refresh` | Inspect and backfill `config.json`. |
| `rules path\|show\|check\|refresh` | Inspect and validate `rules.json`. |
| `credential set\|list\|rm` | Manage credentials and their host bindings. |
| `token show\|rotate\|proxy-credential` | Manage the shared bearer token. |
| `ca export\|rotate\|path` | Manage the interception CA. |

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
