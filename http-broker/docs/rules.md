# Rules

Policy lives in `~/.config/http-broker/rules.json`. Validate with
`http-broker rules check`, apply with `SIGHUP`.

## Shape

```json
{
  "fallthrough": "tunnel",
  "rules": [
    {
      "name": "...",
      "host": "...",
      "mode": "intercept|tunnel|deny",
      "path": "...",
      "method": "...",
      "ports": [443],
      "allow_private": false,
      "min_tls_version": "1.2|1.3",
      "inject": { "set": { "Header": "${cred.name}" }, "remove": ["Header"] }
    }
  ]
}
```

`name` and `host` and `mode` are required. `name` must be unique — audit rows
and the dashboard attribute by it.

## Modes

| Mode        | Effect                                                               |
| ----------- | -------------------------------------------------------------------- |
| `intercept` | Terminate TLS, evaluate each request, optionally inject.             |
| `tunnel`    | Relay opaque bytes. No injection, no method or path is ever visible. |
| `deny`      | Refuse.                                                              |

A `tunnel` rule must be **host-only**: tunnelling is decided at CONNECT, before
any request exists, so it cannot be scoped by `path` or `method`. A path-scoped
`deny` _is_ coherent, because interception can precede rejection.

## Globs

`*` matches within one segment; `**` matches across segments. Patterns are
anchored at both ends, so `github.com` does not match `github.com.attacker.com`.

| Pattern             | Matches                                          | Does not match                   |
| ------------------- | ------------------------------------------------ | -------------------------------- |
| `api.github.com`    | `api.github.com`                                 | `github.com`, `x.api.github.com` |
| `*.github.com`      | `api.github.com`                                 | `github.com`, `a.b.github.com`   |
| `**.github.com`     | `api.github.com`, `a.b.github.com`, `github.com` | `github.com.evil.test`           |
| `/repos/*/*/issues` | `/repos/o/r/issues`                              | `/repos/o/r/issues/1`            |
| `/admin/**`         | `/admin/users/42`                                | `/admin`                         |

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
   `source=implicit-allow` — the host is already known to policy, so
   `fallthrough` does not apply.
5. No rule matches the host: the `fallthrough` policy decides, and the row is
   audited `source=fallthrough`.

The audit log records the two halves of that separately: `source` is what
decided (`rule`, `fallthrough` or `implicit-allow`, with `matched_rule` naming
the rule) and `mode` is what the proxy then did (`intercept`, `tunnel` or
`deny`). A fallthrough decision therefore still says whether the connection was
relayed or refused.

### Ports

`intercept` and `deny` default to **any port**. `tunnel` defaults to **443
only**, and so does a `fallthrough: "tunnel"` decision on a CONNECT — otherwise
it would relay any port an agent names, which is a far larger grant than
choosing to relay unmatched HTTPS. The limit is about tunnelling, so it does
not apply to an absolute-form plain HTTP request, which is parsed, evaluated
and audited whatever port it names.

`deny` matches `intercept` here deliberately. A narrower default would let an
unported `deny` cover only 443 while the `intercept` rule it overrides covers
every port, which would break "deny beats intercept" off 443.

Opt a port in explicitly:

```json
{
  "name": "alt",
  "host": "api.example.com",
  "ports": [443, 8443],
  "mode": "tunnel"
}
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

## Internal hosts

`netguard` refuses any dial into RFC 1918 or RFC 4193 private space, so an
internal service is unreachable until a rule says otherwise:

```json
{
  "name": "svc-prod",
  "host": "svc-prod.ds.aws.example.com",
  "mode": "intercept",
  "allow_private": true,
  "inject": { "set": { "Authorization": "Bearer ${cred.svc_api}" } }
}
```

The grant relaxes **only** the private-address class. Loopback, link-local,
multicast, unspecified, the reserved ranges and the cloud-metadata addresses
stay refused, so `169.254.169.254` is never reachable through policy. The dial
still goes to the resolved-and-validated address, never the hostname.

It is as wide as the glob that carries it: `svc-prod.ds.aws.example.com` grants one
host, `**.example.com` grants everything DNS puts under `example.com`. Keep it narrow.

Three rules of use:

- **Host-only rules only.** The dial is per host and port, so `allow_private`
  alongside a `path` or `method` would claim to be narrower than it is. It is
  rejected at load.
- **`intercept` and `tunnel` only.** A `deny` rule never dials, so the grant
  would be inert.
- **Decided at CONNECT, over every matching rule.** If any rule matching the
  host and port grants it, the connection has it — file order never decides.
  This is also why it cannot vary per request: connections are pooled by host
  and port, so the first request's grant would otherwise bind the rest.

## Upstream TLS floor

The proxy negotiates **TLS 1.3 only** by default, which is stricter than much of
the internet. An AWS ALB on an older security policy tops out at 1.2 and closes
the connection instead of sending a version alert, so the failure arrives as a
bare `EOF` in the audit log rather than anything legible.

Lower the floor for the hosts that need it, not globally:

```json
{
  "name": "svc-prod",
  "host": "svc-prod.ds.aws.example.com",
  "mode": "intercept",
  "allow_private": true,
  "min_tls_version": "1.2",
  "inject": { "set": { "Authorization": "Bearer ${cred.svc_api}" } }
}
```

Accepted values are `"1.2"` and `"1.3"`. TLS 1.0 and 1.1 are deliberately
unavailable. **Verification is unaffected at every floor** — the certificate
must still chain to the system trust store, so this is not `--insecure`.

`intercept` only: a tunnel relays the client's own handshake untouched and a
`deny` rule connects to nothing, so on either the field would be inert. Host-only
only, and among overlapping rules the **lowest** floor wins, so a broad rule
added later cannot silently raise the floor and break a host that needs 1.2.

## Load-time validation

Rejected with an actionable message: a missing `host`, `name` or `mode`; a
`tunnel` rule carrying `path` or `method`; duplicate names; an unknown mode; a
bad glob; a port out of range; `inject` on a non-`intercept` rule; two
identically written host-only rules of differing mode; a host glob that
reduces to a public suffix; `allow_private` on a `deny` rule or on a rule
carrying `path` or `method`; and `min_tls_version` with an unsupported value, on
a non-`intercept` rule, or on a rule carrying `path` or `method`.

**Warned, not rejected:** a rule whose host glob is broader than a referenced
credential's bound hosts. Requests it matches outside that binding will be
refused at injection time, which is worth knowing at load rather than
discovering when an agent hits it.
