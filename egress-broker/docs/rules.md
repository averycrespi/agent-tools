# Rules

Policy lives in `~/.config/egress-broker/rules.json`. Validate with
`egress-broker rules check`, apply with `SIGHUP`.

## Shape

```json
{
  "fallthrough": "tunnel",
  "rules": [
    { "name": "...", "host": "...", "mode": "intercept|tunnel|deny",
      "path": "...", "method": "...", "ports": [443],
      "inject": { "set": {"Header": "${cred.name}"}, "remove": ["Header"] } }
  ]
}
```

`name` and `host` and `mode` are required. `name` must be unique — audit rows
and the dashboard attribute by it.

## Modes

| Mode | Effect |
| --- | --- |
| `intercept` | Terminate TLS, evaluate each request, optionally inject. |
| `tunnel` | Relay opaque bytes. No injection, no method or path is ever visible. |
| `deny` | Refuse. |

A `tunnel` rule must be **host-only**: tunnelling is decided at CONNECT, before
any request exists, so it cannot be scoped by `path` or `method`. A path-scoped
`deny` *is* coherent, because interception can precede rejection.

## Globs

`*` matches within one segment; `**` matches across segments. Patterns are
anchored at both ends, so `github.com` does not match `github.com.attacker.com`.

| Pattern | Matches | Does not match |
| --- | --- | --- |
| `api.github.com` | `api.github.com` | `github.com`, `x.api.github.com` |
| `*.github.com` | `api.github.com` | `github.com`, `a.b.github.com` |
| `**.github.com` | `api.github.com`, `a.b.github.com`, `github.com` | `github.com.evil.test` |
| `/repos/*/*/issues` | `/repos/o/r/issues` | `/repos/o/r/issues/1` |
| `/admin/**` | `/admin/users/42` | `/admin` |

Hosts are compared in canonical form: lowercased, trailing dot stripped, IDN
punycoded, IP literals normalised. A wildcard segment must be ASCII — write the
literal part in punycode (`xn--…`).

A host glob that reduces to an ICANN public suffix after stripping leading
wildcards (`*.com`, `**.co.uk`) is rejected at load.

## The CONNECT decision

At CONNECT only host and port are visible. The target is split and the host
normalised **before** any glob comparison, so a rule naming a bare host matches
on any port the rule admits.

1. Collect rules whose `host` matches and whose `ports` admit the port.
2. **Any `intercept` match wins** — the path is needed to know which rule
   applies, and only interception reveals it.
3. Otherwise a host-only `tunnel` or `deny` rule applies directly. If globs
   overlap without being written identically, **deny wins**, so the outcome
   never depends on file order.
4. Otherwise only path-scoped `deny` rules match: intercept and decide per
   request. A request matching no rule is forwarded **uninjected** and audited
   `mode=implicit-allow` — the host is already known to policy, so
   `fallthrough` does not apply.
5. No rule matches the host: the `fallthrough` policy decides.

### Ports

`intercept` defaults to **any port**. `tunnel` and `deny` default to **443
only**, and so does the `fallthrough` path — otherwise `fallthrough: "tunnel"`
would relay any port an agent names, which is a far larger grant than choosing
to relay unmatched HTTPS.

Opt a port in explicitly:

```json
{ "name": "alt", "host": "api.example.com", "ports": [443, 8443], "mode": "tunnel" }
```

A rule whose `ports` exclude the request port simply does not apply; the
`fallthrough` policy governs. The operator scoped that rule deliberately.

## Per-request evaluation

Once intercepted, each request is matched again with method and path available.

**Deny beats intercept, regardless of rule order.** Otherwise ordering silently
decides whether a credential reaches a path the operator believed was denied.

## Injection

```json
"inject": {
  "set":    { "Authorization": "Bearer ${cred.gh_bot}" },
  "remove": ["X-Agent-Hint"]
}
```

`remove` is applied before `set`, so a rule can strip a header and set its own
value for the same name.

Every `${cred.<name>}` is resolved **and host-checked before any header is
written**. If one fails, nothing is sent — a partially injected request can
never reach the wire.

A credential value is inserted literally and never rescanned, so a value that
happens to contain `${cred.other}` cannot pull in a second credential.

## Load-time validation

Rejected with an actionable message: a missing `host`, `name` or `mode`; a
`tunnel` rule carrying `path` or `method`; duplicate names; an unknown mode; a
bad glob; a port out of range; `inject` on a non-`intercept` rule; two
identically written host-only rules of differing mode; and a host glob that
reduces to a public suffix.

**Warned, not rejected:** a rule whose host glob is broader than a referenced
credential's bound hosts. Requests it matches outside that binding will be
refused at injection time, which is worth knowing at load rather than
discovering when an agent hits it.
