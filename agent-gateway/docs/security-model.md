# Security model

This document describes what agent-gateway protects against, what it doesn't, and what you have to do as the operator to get the guarantees below. It's the companion to the code-level contract in [`DESIGN.md §10`](../DESIGN.md).

## The confused-deputy model

agent-gateway holds your real credentials on behalf of sandboxed AI agents that must never see them. Agents are handed **dummy** credentials (e.g. `Authorization: Bearer dummy`) and send HTTPS traffic through the gateway as an `HTTPS_PROXY`. The gateway MITMs traffic matching loaded rules, verifies the request is one this agent is allowed to make to this host, and **swaps the dummy for the real value** before forwarding to the upstream.

Everything the gateway does is in service of one goal: **a real credential never reaches a request that wasn't authorized for that credential**, even when the agent is deliberately trying to trick the gateway into substituting one.

## Trust boundaries

- **Untrusted.** The sandboxed agent. It can send arbitrary traffic, craft hostile `Host:` headers, exploit ambiguous hostname encodings, or misuse any legitimate-looking pattern that a rule might match.
- **Trusted.** The gateway process, its on-disk config (`config.hcl`, `rules.d/`), the encrypted secret store, the master key (OS keychain or `0o600` file), and whoever holds the admin token.
- **Out of scope.** Anything else: the host OS, keychain access control, network between the gateway and upstream, upstream server behavior.

If an attacker has code execution on the host, they have the master key — and through it, every real credential. The gateway can't defend against that; it's a pre-condition you must maintain.

## What the gateway guarantees, when correctly configured

Each property is a commitment — a thing that will not happen regardless of what the agent sends.

1. **Dummy in, real out — only when a rule says so.** If no rule matches a request, the dummy credential reaches the upstream untouched. Unmatched requests fail upstream as unauthenticated, not as forged. Forgotten routes are safer than forgotten credentials.
2. **Scoped secrets stay scoped.** Every secret is bound to an explicit `allowed_hosts` list at creation. If a rule somehow matches for a host outside that list, the gateway returns `403 Forbidden` and does **not forward the request**. This is deliberate: a hard failure makes the misconfig obvious; a soft failure would masquerade as an upstream 401 and hide the bug.
3. **Hostname rules are strict.** A rule literal like `api.github.com` never accidentally matches `api.github.com.attacker.com`, `GITHUB.COM`, `xn--` punycoded variants, or `api.github.com.`. All hostname inputs are canonicalized (IDNA, lowercased, trailing dot stripped) before any comparison, and glob patterns compile to anchored regexes with no substring semantics.
4. **The CONNECT target is the rule target.** Once an agent has issued `CONNECT api.github.com:443`, a `Host: attacker.com` header inside the TLS tunnel cannot steer injection elsewhere. Rule evaluation uses the CONNECT hostname, not the `Host:` header.
5. **No redirect following.** Upstream responses — including `3xx` redirects — are returned to the agent verbatim. An agent that follows a redirect opens a fresh CONNECT, which re-enters the gateway's decide → match → inject pipeline from scratch. A malicious upstream cannot use a `302` to smuggle injected credentials to an attacker-controlled host.
6. **Body-matched rules fail closed.** If a rule's body matcher can't run because the body is larger than `max_body_buffer` or buffering timed out, the request is blocked with `403`, even if the rule's verdict was `allow`. The audit row records the bypass. A body condition we can't evaluate is never silently skipped.
7. **Bad rule edits don't break running traffic.** A malformed HCL file found during `rules reload` is logged and rejected; the previous ruleset stays live. In-flight requests finish on the ruleset they started with.
8. **Agents can't impersonate each other.** Every CONNECT verifies `Proxy-Authorization` in constant time against the argon2id-hashed token store. Agent-scoped secrets and per-agent rule scopes resolve by the authenticated agent name, not by any field the agent controls.

## What you have to do right

The guarantees above assume you've done the following. None of these are optional.

- **Bind every secret to the narrowest host globs that work.** `secret set <name> --host api.github.com` is better than `--host "*.github.com"` is better than `--host "**"`. A narrow scope is the primary defense against a confused rule injecting into the wrong place. `--host "**"` is accepted but you're opting out of the host-scope guarantee for that secret.
- **Review `no_intercept_hosts` before every deploy.** Entries there disable MITM entirely. The daemon hard-rejects wildcard-only entries and soft-warns on entries that reduce to a public suffix (`*.com`, `*.co.uk`, etc.), but still accepts them — read warnings on startup. A broad `no_intercept_hosts` is a silent rule bypass.
- **Read soft warnings.** `rules check` output and daemon startup logs include warnings for `host = "**"`, public-suffix `no_intercept_hosts`, and secret `allowed_hosts` that don't obviously cover their referencing rule's `match.host`. Treat these as "someone should look at this," not noise.
- **Keep the dashboard on loopback.** The dashboard binds to `127.0.0.1:8221` by default. Don't move it off loopback without putting it behind an authenticated reverse proxy. The admin token gates write actions (approve/deny) but the live feed leaks host names and rule hits to anyone who can reach the port.
- **Rotate on agent departure.** `agent rm <name>` invalidates a sandbox's token immediately. Don't leave stale agent tokens with rule scope they no longer need.
- **Rotate the root CA if a sandbox is compromised.** A compromised sandbox has your CA cert in its trust store. That only matters if the sandbox is now used as a launching point for MITM against *other* systems in your environment, but if that's on the table, `ca rotate` and re-provision.

## What's out of scope

agent-gateway does **not** protect against any of the following. Assume these are your problem, not the gateway's:

- **Host OS compromise.** An attacker with user-level code execution on the host has the master key and can decrypt the secret store directly.
- **Admin-token theft from disk.** The admin token is stored at `$XDG_CONFIG_HOME/agent-gateway/admin-token` with mode `0o600`. Filesystem permission is the only barrier.
- **Upstream server compromise.** Credentials injected into legitimate destinations can still be misused by a compromised or malicious upstream. The gateway has no visibility once a request leaves its process.
- **Traffic analysis.** Destination hostnames and timing are visible to anyone on the network between the gateway and upstream. Gateway → upstream traffic is TLS but SNI is not hidden.
- **Denial of service.** No rate limiting on agent-facing ports. A misbehaving or malicious sandbox can exhaust gateway resources; isolate sandboxes that might do this at the OS level.
- **Post-injection detection.** Once a secret has been legitimately injected into a request that matched a rule, the gateway has no further visibility into how the upstream handles it. If the upstream logs the credential, forwards it, or is compromised, that's outside the gateway's scope.

## Deployment assumptions

The security properties above assume a specific deployment posture:

- The gateway runs on a trusted host, alongside the sandboxed agents it serves.
- Proxy and dashboard ports bind to loopback (`127.0.0.1`) by default; changing that is a deliberate choice, not an accident.
- Sandboxes are configured by `sandbox-manager` or equivalent, which installs the gateway's root CA into the sandbox's trust store and sets `HTTPS_PROXY` to the gateway.
- One operator, or a small trusted team, holds the admin token. There is no multi-user model in v1.
- Secrets are set with the narrowest `allowed_hosts` that permit the legitimate rule to fire, and rules are written from a deny-by-default mental model (start specific, widen when needed).

If any of these assumptions doesn't hold in your environment, the guarantees above are weakened in predictable ways. See the mechanism details in [`DESIGN.md`](../DESIGN.md) to reason about specific deviations.
