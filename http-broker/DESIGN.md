# http-broker design

Source of truth for what this tool should be and do. When the code and this
document disagree, the code is the bug.

## Motivation

`mcp-broker` solved one shape of the problem: an agent calls a tool, the broker
holds the credential, the agent never sees it. That covers MCP tool calls and
nothing else. An agent that reaches for `curl`, an SDK, or any ordinary HTTP
client is back to holding its own secrets.

`http-broker` applies the same premise to raw HTTP. It sits between the
sandbox and the internet, decides per connection whether to intercept, tunnel,
or deny, injects credentials the sandbox never holds, and records every request
to an audit log.

## The guarantee, and its limit

Enforcement is **cooperative**. It rests on the sandbox honouring
`HTTP_PROXY`/`HTTPS_PROXY`. An agent that unsets them, uses `curl --noproxy`,
or opens a raw socket bypasses this tool entirely.

This matches `sandbox-manager/DESIGN.md`, which states the sandbox "is not a
data-loss-prevention boundary" and that guest egress is intentionally allowed.
http-broker makes credential handling safe for cooperating traffic and makes
that traffic auditable. **It is not a containment boundary.**

Network-level enforcement — guest firewall rules permitting egress only to the
proxy — is the honest v2 direction. Until that exists, the audit log records
cooperating traffic only, and its completeness should not be assumed.

The likeliest failure of this tool is not a breach. It is quiet uselessness:
staying on `fallthrough: "tunnel"` forever, producing a log that looks complete
while missing anything uncooperative, and creating false confidence. Tightening
toward `deny` with an explicit allowlist is the intended end state.

## Architecture

One binary, two loopback listeners.

- **`:8220`** accepts HTTP `CONNECT` and absolute-form plain HTTP.
- **`:8221`** serves the read-only dashboard and its SSE feed under
  `/dashboard/`, plus the unauthenticated `/ca.pem` and `/healthz` at the root.
  The root itself redirects to `/dashboard/`.

**Two ports, not one.** `mcp-broker` shares a port because MCP and its
dashboard are both ordinary HTTP. A forward proxy is not, and sharing would
make the dashboard reachable _through_ the proxy.

**The `/dashboard/` prefix is consistency, not necessity.** With a dedicated
listener nothing else could collide, so the prefix earns its place only by
matching `mcp-broker`, where the same port also serves `/mcp`. `/healthz` and
`/ca.pem` stay at the root because their consumers are monitors and
provisioning scripts, which should not have to track a UI path.

```
cmd/http-broker/     CLI entry point (Cobra), composition root in serve.go
internal/
  paths/               XDG config/data split
  config/              config.json plus rules-loading orchestration
  rules/               rule schema, validation, and the matching engine
  glob/                the single glob-to-regexp translation
  hostnorm/            host and host-glob canonicalisation
  hostmatch/           public-suffix detection
  netguard/            SSRF and cloud-metadata guard for both dial paths
  ca/                  root CA and leaf issuance
  credentials/         resolution and host binding
  auth/                token storage plus the two auth checks
  proxy/               CONNECT handling, tunnel relay, MITM, request pipeline
  audit/               SQLite store
  dashboard/           embedded read-only UI
```

## Key design decisions

### Three modes, one rules list

Each rule matches a mandatory `host` glob plus optional `path`, `method` and
`ports`, and carries `mode: intercept | tunnel | deny`. A single list is easier
to reason about than a mode plus a separate exclusions list, which is what the
predecessor had.

### The CONNECT decision is host-keyed

At CONNECT only host and port are visible. The target is split and the host
normalised _before_ any glob comparison, so rules never match a `host:port`
string. The five-step procedure is documented in `docs/rules.md`.

A rule whose `ports` exclude the request port simply does not apply — the
fallthrough policy governs. Treating a non-matching port as a refusal would
override the scoping the operator deliberately wrote.

### Deny beats intercept, regardless of order

Otherwise rule ordering silently decides whether a credential reaches a path
the operator believed was denied, and reordering a file for readability changes
enforcement.

The same principle applies at CONNECT: load validation only catches two
host-only rules whose globs are written _identically_, so overlapping globs of
differing mode (`api.example.com` and `*.example.com`) resolve to deny rather
than to whichever came first.

### One credential reference form, two sources

Rules always write `${cred.<name>}`. A record is `{value, hosts}` and comes
from the OS keychain or an `env_credentials` block. **Both carry bound hosts
and go through the identical check.**

An earlier draft had rules reference `${keychain.*}` and `${env.*}`
separately, which silently exempted environment-sourced credentials from host
binding. The single enforcement path exists to make that class of gap
impossible rather than documented.

### The keychain resolves the launchd problem

launchd does not source shell profiles, so an environment-only design forces
every API key into `~/Library/LaunchAgents/*.plist` in plaintext. With the
keychain the plist carries `PATH` and nothing else.

An unavailable keychain is a hard error, never a silent file fallback. The
predecessor fell back to a `0600` file, producing exactly the
plaintext-on-disk outcome this design rejects. `env_credentials` exists so the
test suite never touches a real keychain and headless Linux stays workable; it
carries the same host binding, and `serve` warns about it at startup.

### Host binding is a second, independent check

The rule's `host` glob decides _whether a rule fires_. The credential's bound
globs decide _whether this credential may go to this host_. Both must pass.
Without the second check a single rule-authoring slip sends a real token
wherever the rule now matches.

### Injection is atomic

All templates for a rule are expanded and host-checked before any header is
written. A single failure discards the whole in-progress request, so a
partially injected request can never reach the wire.

### Internal hosts are reachable only by naming them

`netguard` refuses every dial into RFC 1918 and RFC 4193 private space, which
also refuses the internal services an agent most often needs a credential for.
An internal ALB and an SSRF attempt are indistinguishable by address; only a
rule that names the host can tell them apart.

So a rule may set `"allow_private": true`, and that grant relaxes exactly the
private-address class for the hosts its glob matches. Loopback, link-local,
multicast, unspecified, the reserved prefixes and the explicit cloud-metadata
addresses stay refused, and the dial still goes to the resolved-and-validated
address rather than the hostname, so the DNS-rebinding guarantee is unchanged.
The predecessor's `allowPrivate` disabled every check except IMDS; this is
deliberately not that.

Three properties make the grant safe to reason about:

- **It is written on a host-only rule.** The dial is per host and port, so a
  path- or method-scoped grant would read as narrower than it is. Load-time
  validation rejects that form rather than silently widening it.
- **It is decided at CONNECT, over every matching rule.** `http.Transport`
  pools connections by host and port and knows nothing about context, so a
  per-request grant would bind only whichever request dialled first.
  Accumulating it over all matching rules also keeps file order out of it, the
  same reason deny beats intercept.
- **It is not implied by `mode`.** An intercept rule means "inject a credential
  here", which is a different claim from "this host lives inside my network".
  Fusing them would grant private resolution to every intercepted host,
  including ones whose rules were written years earlier for unrelated reasons.

### The dashboard opens itself, but only from a terminal

`serve` prints the dashboard URL with a `?token=` parameter and opens it in a
browser, matching `mcp-broker`. The dashboard swaps the token for a cookie and
redirects to the same path without it, so the token never enters browser
history.

Both steps are gated on stdout being a terminal, which `mcp-broker` does not do.
The intended deployment is launchd, where stdout is a log file: printing the URL
there would persist the token in a world-readable log, and opening a browser at
every login is the wrong behaviour for a supervised daemon. `--no-open` remains
the explicit control for interactive runs, and the shipped plist passes it.

### Recovery is a rules edit, not a process kill

Proxy variables are baked into every sandbox's shell, so killing `serve` points
them all at a dead socket and causes total network loss — worse than the
misbehaviour prompting the kill. The kill switch is an all-tunnel `rules.json`
plus `SIGHUP`.

## Prior art

### `origin/agent-gateway` (this repository)

A substantially complete earlier implementation: 192 commits, roughly 12,000
production lines, last touched 2026-04-24. Four packages are ported from it
with attribution — `ca`, `netguard`, `hostnorm`, `hostmatch` — because they
carry edge-case coverage (SSRF, DNS rebinding, IDN, public suffixes) that is
expensive to re-derive. Everything else was written fresh against a smaller
model; `secrets`, `approval` and `agents` were dropped outright.

**Why that branch was never merged is not recorded anywhere.** No pull request
was opened for it; the repository's only PR is unrelated. Its final commit is
`chore: remove completed plan files`, which deleted its own `.plans/` and
`.designs/` entries, after which the branch went dormant with no note, no
revert, and no issue. The history simply does not say.

This matters because if the cause was the maintenance burden of a hand-rolled
MITM stack, that reason applies here too. This design is materially smaller —
no approval subsystem, no encrypted secret store, no per-agent identity, no
HCL — but it carries the same standing cost, named below. **If you know the
actual reason, record it here.**

Ported code was not trusted on reputation. That branch's `internal/proxy`
carried roughly 2,950 test lines and still shipped an unguarded `net.Dial` on
its tunnel path, leaving cloud metadata reachable. Every ported package got
independent adversarial tests, and those tests found real gaps the source had:
NAT64/6to4/Teredo addresses embedding a blocked IPv4 address, carrier-grade NAT
(where Alibaba Cloud's metadata service lives), and two spellings of one IPv6
address surviving as distinct strings past a host check.

Its `DESIGN.md` contradicts its own code in several places. Read its code, not
its docs.

### External

Concepts re-implemented in Go with no code copied:

- [Infisical/agent-vault](https://github.com/Infisical/agent-vault) — the
  six-CA-environment-variable insight, resolve-then-dial-the-validated-IP, and
  a metadata-only audit sink.
- [onecli](https://github.com/onecli/onecli) — the
  `Proxy-Authorization: Basic base64("x:<token>")` convention and the CA/leaf
  issuance model.

## Standing costs

**A hand-rolled HTTP/2 MITM terminator needs CVE tracking.** The predecessor
required explicit Rapid Reset (CVE-2023-44487) and CONTINUATION-flood
(CVE-2024-27316) hardening. This implementation bounds concurrent streams,
frame size, header-table size and per-stream upload buffer, and inherits fixes
from `golang.org/x/net/http2` — but that dependency must be kept current, and
new HTTP/2 denial-of-service classes will need review. This is an accepted,
ongoing cost, not a solved problem.

**Certificate-pinning clients cannot be intercepted.** They fail at the TLS
handshake by design. The fix is a `mode: "tunnel"` rule; the README documents
the error signature.

**CA rotation invalidates every provisioned sandbox** with no overlap window.

## Non-goals

Human-in-the-loop approval (belongs in `mcp-broker`, where arguments are
visible), learned rules or dashboard-driven policy writing, an encrypted secret
store, per-agent identity, header-regex and request-body matchers, injection
into path/query/body, WebSocket through MITM, response rewriting, redirect
following, rate limiting, non-HTTP protocols, and network-level egress
enforcement.
