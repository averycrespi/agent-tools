# Security model

## Trust boundaries

Four parties, and what each is trusted with.

| Party | Trusted with | Not trusted with |
| --- | --- | --- |
| **The host** | The CA private key, the OS keychain, `config.json`, `rules.json`, the audit database. | — |
| **egress-broker** | Reading credentials, terminating TLS for intercepted hosts, injecting headers. | — |
| **The sandboxed agent** | Reaching the proxy with a shared bearer token. | Any credential value. Any policy decision. The CA private key. |
| **Upstream services** | Receiving injected credentials, subject to each credential's host binding. | Being reached at all unless policy allows it. |

The boundary that matters is between the agent and the credential. The agent
holds a proxy token, which authenticates it to the proxy and nothing else. A
stolen proxy token lets an attacker use policy as written — it does not reveal
a single credential value, and it cannot widen policy.

**The proxy listener is loopback-only and enforced as such at startup.** That
network boundary is load-bearing; the bearer token is defence in depth.
Sandboxes reach it through Lima's forwarding of `host.lima.internal` to host
loopback.

## Guarantees

1. **The agent never holds a credential.** Values live in the OS keychain (or,
   for headless hosts, a named environment variable) and are read by the proxy
   at request time. Nothing writes one into the sandbox.
2. **Every credential carries host binding, whatever its source.** Keychain and
   `env_credentials` entries produce the same record and go through the same
   check. Injecting into a request whose canonical host falls outside the
   binding is refused with 403 and audited.
3. **Injection is atomic.** Every credential a rule references is resolved and
   host-checked before any header is written, so a partially injected request
   can never be dispatched.
4. **Deny beats intercept, regardless of rule order.** Reordering `rules.json`
   for readability cannot change whether a credential reaches a path.
5. **Credential values never leave the process.** Not in a log line, not in an
   audit row, not in a dashboard response, not in `credential list` output.
   Enforced by tests that inject a sentinel and sweep every sink.
6. **The audit log cannot become a leak channel.** No column can hold a request
   body, a response body, or a header value. Query strings are stored with
   credential-shaped parameters redacted, including the AWS and Google
   presigned-URL names.
7. **Upstream dials are guarded on both paths.** The tunnel relay and the MITM
   transport share one guard covering cloud metadata, loopback, private,
   carrier-grade NAT, link-local, multicast, unspecified and reserved ranges;
   alternate IP encodings; IPv6 transition forms that embed an IPv4 address;
   and DNS rebinding, by validating every resolved address and dialling the
   validated one.
8. **Upstream TLS is strict.** System trust store, verification on, TLS 1.3
   minimum. Interception does not weaken the connection that actually carries
   the credential.
9. **The dashboard is read-only.** No endpoint writes config, rules, or
   credentials.
10. **A failed reload keeps the previous policy serving.** A typo cannot take
    the sandbox's network down.

## Out of scope

These are **not** guarantees. Do not rely on them.

- **This is not a containment boundary.** Enforcement is cooperative: it rests
  on the sandbox honouring `HTTP_PROXY`/`HTTPS_PROXY`. An agent that unsets
  them, runs `curl --noproxy`, or opens a raw socket bypasses this tool
  entirely. `sandbox-manager/DESIGN.md` states the sandbox "is not a data-loss
  prevention boundary" and that guest egress is intentionally allowed.
  Network-level enforcement — guest firewall rules permitting egress only to
  the proxy — is the v2 direction and does not exist yet.
- **The audit log is not complete.** It records cooperating traffic. Traffic
  that bypassed the proxy is invisible, so an empty log is not evidence that
  nothing happened.
- **Tunnelled traffic is not inspected.** A `mode: "tunnel"` rule relays opaque
  bytes; only host, port, byte counts and duration are recorded.
- **No human-in-the-loop approval.** That belongs where arguments are visible,
  which is `mcp-broker`.
- **No per-agent identity.** One shared token. Every sandbox with it has the
  same policy.
- **No request-body inspection or matching**, and therefore no protection
  against exfiltration through a request body to an allowed host.
- **No response rewriting, redirect following, or rate limiting.**
- **The `EGRESS_BROKER_TEST_ALLOW_ADDRS` variable disables the address guard**
  for the exact targets it names. It exists so the test suite can reach a mock
  upstream on loopback. Never set it in production; `serve` logs a warning
  naming every exemption at startup.

## Compromise response

### A leaked bearer token

The token authenticates to the proxy. Anyone holding it can use policy as
written — they cannot read a credential or change policy.

```bash
egress-broker token rotate
```

Then re-provision every sandbox and `SIGHUP` the running process. Until a
sandbox is re-provisioned its requests get 407. Review the audit log for
requests you cannot account for.

### A leaked `ca.key`

The most serious of these. Anyone holding it can issue certificates every
provisioned sandbox will trust, and so can intercept sandbox traffic
undetected.

```bash
egress-broker ca rotate --yes
```

There is **no overlap window**: every sandbox stops trusting the proxy the
moment rotation completes, and TLS interception fails there until provisioning
is re-run in each one. Re-provision every sandbox, then `SIGHUP`.

Then treat every credential the proxy could inject as exposed and rotate it at
its provider — a holder of the CA key could have read them in flight.

### An exposed `audit.db`

By design it holds no credential values, no bodies, and no header values, and
query strings arrive redacted. The exposure is metadata: which hosts were
reached, when, how often, with what outcome.

Verify what was actually in it before concluding anything:

```bash
sqlite3 ~/.local/share/egress-broker/audit.db 'SELECT DISTINCT host FROM audit_records'
```

Check the `-wal` and `-shm` sidecars too — they hold the same content and are
created with the same `0600` mode. If any file was world-readable, treat the
host reachability history as disclosed and rotate nothing else.

### A leaked credential

1. Rotate it at the provider first. That is the only step that actually revokes
   access.
2. Store the replacement: `printf %s "$NEW" | egress-broker credential set <name> --host <host>`
3. `SIGHUP` the running process to drop the cached old value.
4. Query the audit log for what the credential was used for:
   `SELECT ts, host, path, status FROM audit_records WHERE credential_ref LIKE '%<name>%'`

The audit log tells you which hosts it reached through this proxy. It cannot
tell you anything about use outside the proxy.
