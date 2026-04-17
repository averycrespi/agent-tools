# agent-gateway — Design

**Status:** draft · **Date:** 2026-04-16 · **Author:** Avery Crespi

## 1. Summary

`agent-gateway` is a host-native Go service that acts as a man-in-the-middle
HTTP/HTTPS proxy for sandboxed AI agents. Sandboxes are handed dummy
credentials; the gateway matches outgoing requests against declarative rules
and swaps dummy credentials for real ones at request time.

It generalises what `mcp-broker` does for MCP tool calls to all HTTP traffic,
and takes implementation cues from onecli while adding (a) rule matching on
headers and body, (b) a per-request audit log, and (c) a match-and-swap model
where unmatched routes pass through with the dummy credential intact (and
therefore fail upstream naturally) rather than leaking real credentials.

## 2. Goals & Non-goals

### Goals (v1)

- Transparent-from-the-agent's-perspective HTTP/HTTPS proxy with TLS MITM via
  an on-disk root CA.
- Rules match on host, method, path, headers, and **content-type-aware body
  fields** (JSON-path for JSON, regex for form/text bodies) — a strict
  superset of onecli's capability.
- Three verdicts: `allow` (with credential injection), `deny`, and
  `require-approval`.
- HCL-authored rules loaded from a directory, hot-reloaded via `fsnotify`.
- SQLite-backed encrypted secret store; master key stored in the OS keychain
  (file fallback if keychain is unavailable).
- Per-agent identity via tokens, with per-rule and per-secret scoping.
- Embedded web dashboard (vanilla JS + SSE), read-only for rules/agents/
  secrets; the only write is approve/deny of pending requests.
- HTTP/2 MITM support end-to-end (modern LLM APIs require it).
- Match-and-swap model: unmatched hosts tunnel without MITM; unmatched routes
  on matched hosts pass through with the dummy credential intact.
- Host-native single Go binary; XDG-conformant paths; single SQLite file for
  all state.

### Non-goals (v1)

Each deferred to v1.1+ with a one-line rationale so future-us has the context.

- **Request body rewriting.** Headers-only injection for v1. Body rewriting is
  correctness-hostile (content-length, signing, multipart, gRPC) and we don't
  yet have a concrete use case.
- **Rate-limiting / quota verdicts.** onecli supports these; we'll add if a
  concrete need appears (e.g. capping agent LLM spend).
- **Telegram/Slack approvers.** Dashboard is the only approval UI in v1.
  Out-of-band approvers are additive; easy to add when someone asks for it.
- **Docker / compose packaging as a first-class target.** Host-native binary
  is the primary distribution; Docker is a later "also runs here" target.
- **Multi-user / team features**, OAuth-based dashboard auth. Single-user
  local-dev is the v1 audience.
- **External vault providers** (1Password, Bitwarden, HashiCorp Vault). SQLite
  - AES-GCM + OS keychain covers single-user locally.
- **`/bootstrap.sh` endpoint.** Shell bootstrap is sandbox-manager's job; the
  gateway exposes `/ca.pem` and `agent-gateway ca export` only.
- **`POST /agents` HTTP API.** Agent creation is CLI-only in v1. Integrations
  shell out to `agent-gateway agent add`. Adding an HTTP API later is additive.
- **Agent groups / labels.** Rules target agents by name list in v1. If the
  list grows long, groups come later.
- **Multipart, protobuf, gRPC body matching.** Schema-aware inspection is
  infeasible in v1; `json_body`/`form_body`/`text_body` cover the common APIs.
- **Explicit scope-pinning in rule templates** (e.g. `${secrets.global.X}`).
  Rules rely on implicit most-specific-wins resolution; can add pinning
  syntax later without breaking existing rules.

### Success criteria

- Sandbox points `HTTPS_PROXY` at the gateway and trusts the gateway CA; a
  rule of the form "inject `${secrets.gh_bot}` for `POST api.github.com/
repos/*/issues`" works against a real sandbox running `gh` without the
  sandbox ever seeing the real token.
- Audit log and live SSE feed show every intercepted request with
  matched-rule attribution, verdict, and which credential-by-name was
  substituted (never the value).
- Rotating a secret via CLI takes effect on the next request (no restart).
- Rule file edits reload hot, and invalid edits leave the previous rule-set
  live (fail-safe reload).

## 3. Architecture

### Components (all in one Go binary)

```
cmd/agent-gateway/      CLI: serve, agent {add,list,rm,rotate,show},
                             secret {set,list,rotate,rm,master rotate,export},
                             rules check, token rotate admin, ca {export,rotate},
                             config {path,edit,refresh}

internal/proxy/         MITM HTTP/HTTPS proxy, CONNECT handler, per-host
                        *tls.Config cache, ALPN (h1 + h2), body buffering
internal/ca/            Root CA load/generate, leaf issuance (24h, 1h refresh buffer)
internal/rules/         HCL loader (directory glob), matcher, fsnotify hot-reload,
                        first-match-wins ordered evaluation
internal/inject/        Header verbs (set_header, remove_header),
                        ${secrets.X} / ${agent.X} template expansion
internal/secrets/       SQLite-backed AES-256-GCM store, master key via
                        go-keyring (file fallback)
internal/audit/         SQLite WAL, metadata-only rows, indexed by
                        (agent, host, ts) and (matched_rule, ts)
internal/agents/        Agent registry (name, token_hash, prefix, last_seen)
internal/dashboard/     Embedded SPA (vanilla JS + SSE), read-only views +
                        approve/deny
internal/approval/      In-memory pending-request store, 5-min timeout,
                        dashboard is the only approver in v1
internal/config/        XDG-aware, rules dir glob, ports, log, timeouts
```

### Ports

- **`:8220` proxy** — HTTP CONNECT + plain HTTP. Bound `127.0.0.1` by default.
- **`:8221` dashboard** — dashboard SPA, `/ca.pem`, SSE. Bound `127.0.0.1` by
  default.

Both override-able via `config.hcl` and CLI flags. Ports chosen to be adjacent
(sibling-tool recognisable) and non-conflicting with `mcp-broker`'s `:8200`.

### Request lifecycle (HTTPS MITM path)

1. Agent opens TCP to `:8220`, sends
   `CONNECT api.github.com:443 HTTP/1.1` with
   `Proxy-Authorization: Basic base64("x:agw_…")`.
2. Proxy validates agent token → resolves agent name. Looks up whether
   `api.github.com` appears in any currently-loaded rule's host glob. **No
   match → pure TCP tunnel** (audit row with bytes-in/out + duration only).
3. Match → MITM: 200 OK back to agent; handshake using a cached-or-issued leaf
   cert for `api.github.com` signed by our root CA; ALPN advertises
   `h2,http/1.1`.
4. Request decoded (h2 frames or h1). Matcher evaluates rules in filename
   order × within-file order → first match wins → verdict.
5. Verdict dispatch:
   - **allow**: apply `inject` block (set_header / remove_header with
     `${secrets.X}` / `${agent.X}` expansion) → dial upstream using system
     trust store with strict TLS verification → stream request → stream
     response → audit.
   - **deny**: synthesise `403 Forbidden`, audit.
   - **require-approval**: park request in approval store, push SSE event to
     dashboard, block until decision or 5-minute timeout. Approved → continue
     as allow. Denied or timed-out → `403` (timeout is `504 Gateway Timeout`).
6. Unmatched request on a MITM'd host: pass through untouched (dummy
   credential intact) → audit with `matched_rule=NULL`. This is the
   fail-safe: forgotten routes fail upstream as unauthenticated rather than
   leaking real credentials.

### Agent-to-gateway authentication

Agent token travels in the proxy URL's userinfo, not in an explicit header:

```bash
export HTTPS_PROXY=http://x:agw_a1b2c3…@host.internal:8220
export HTTP_PROXY=http://x:agw_a1b2c3…@host.internal:8220
```

Every well-behaved HTTP client (`gh`, `curl`, `git`, Node, Python-requests,
Go `net/http`) converts this into `Proxy-Authorization: Basic base64("x:agw_…")`
on CONNECT. The `x:` is an arbitrary placeholder username (HTTP clients
won't accept a URL with a password but no username); onecli uses the same
convention.

Known limitation: tools that explicitly bypass proxy env vars (some Go
binaries with `net/http.Transport{Proxy: nil}`, certain pinned mobile SDKs)
will escape the gateway entirely. Documented as a v1 limitation; iptables
REDIRECT is a possible v2 add-on.

### Concurrency

Per-connection goroutine for CONNECT. Shared `sync.Map` leaf-cert cache.
Rules, agents, and secrets snapshots live in `atomic.Pointer[state]`; readers
never block, reloads swap the pointer, in-flight requests finish on the old
snapshot.

## 4. Rule Model

### File layout

Rules live under `~/.config/agent-gateway/rules.d/*.hcl`. One file per
upstream is the intended workflow (`github.hcl`, `atlassian.hcl`,
`anthropic.hcl`). Loader concatenates in lexical filename order; within a
file, rules evaluate top-to-bottom. First match wins. Prefixing files with
`00-`, `10-`, `20-` gives a predictable priority knob.

### HCL schema

```hcl
rule "github-issue-create" {
  agents = ["claude-review", "codex-sandbox"]  // optional; default = all

  match {
    host   = "api.github.com"              // glob: * within segment, ** multi-segment
    method = "POST"                        // optional; default = any
    path   = "/repos/*/*/issues"

    headers = {                            // name -> regex (RE2); AND
      "X-GitHub-Api-Version" = "^2022-"
    }

    json_body {                            // implies Content-Type: application/json
      jsonpath "$.title"     { matches = "^\\[bot\\]" }
      jsonpath "$.labels[*]" { matches = "^automation$" }
    }
  }

  verdict = "allow"                        // allow | deny | require-approval

  inject {                                 // only for allow / require-approval
    set_header    = { "Authorization" = "Bearer ${secrets.gh_bot}" }
    remove_header = ["X-Agent-Hint"]
  }
}
```

#### Body matchers (one per rule, choose one)

```hcl
json_body {
  jsonpath "$.title" { matches = "..." }
}

form_body {                                // application/x-www-form-urlencoded
  field "grant_type" { matches = "^client_credentials$" }
}

text_body {                                // text/*
  matches = "deploy-token-v2"              // regex over raw body
}
```

If the actual `Content-Type` doesn't match the declared block type, the rule
fails to match (no silent coercion). Out of scope for v1: multipart,
protobuf, gRPC.

### Matching semantics

- `host`, `path`: globs, `*` within a segment, `**` across segments.
  Compiled at load time.
- `method`: exact, uppercase.
- `headers`, `json_body`/`form_body`/`text_body` matchers: Go `regexp` (RE2).
  All declared matchers must succeed (AND).
- Body matchers require buffering the request body up to
  `proxy_behavior.max_body_buffer` (default `1MiB`); beyond the cap, body
  matchers auto-fail and a warning is logged.

### Verdict precedence

First-match-wins across filename order then within-file order. No separate
"pass evaluation" sorting by verdict type. If ordering matters, express it
explicitly via filename prefixes.

### Template expansion

Only at injection time. Variables:

- `${secrets.<name>}` — resolved against the secrets table.
- `${agent.name}`, `${agent.id}` — the calling agent.

Undefined variables are **load-time** errors, not request-time. Validation
runs during `agent-gateway rules check` and on hot-reload. An invalid
reload is rejected; the previous valid rule-set stays live.

### Injection verbs

Two in v1:

- `set_header` — create-or-overwrite. `{ "Name" = "value" }`.
- `remove_header` — `["Name1", "Name2"]`.

onecli's `ReplaceHeader` (set-only-if-present) is redundant because the
same condition can be expressed in the `match` block:

```hcl
match {
  headers = { "Authorization" = "^Bearer " }   // match only requests that DID auth
}
inject {
  set_header = { "Authorization" = "Bearer ${secrets.gh_bot}" }
}
```

This keeps intent visible in code review.

### Hot reload

`fsnotify` on `rules.d/`. On any create/modify/delete:

1. Re-parse the whole directory.
2. Validate template references against the live agents/secrets tables.
3. On success: swap the `atomic.Pointer[ruleset]`. In-flight requests finish
   on the old set; new requests use the new set.
4. On failure: log error to stderr + push a dashboard event; keep previous
   rule-set live.

### CLI

```
agent-gateway rules check
agent-gateway rules check --request '{"host":"api.github.com","method":"POST","path":"/repos/org/repo/issues",...}'
```

The dry-run form prints the matched rule (and why) for a synthetic request.
Debugging aid for rule authoring.

## 5. Secrets

### Master key

On first run, `internal/secrets` generates a 32-byte random key and attempts
to store it in the OS keychain (`go-keyring`; service `agent-gateway`,
account `master-key`). Fallback: `~/.config/agent-gateway/master.key` at
mode `0600`, with a prominent startup warning. `master.key` is only present
when keychain is unavailable.

`agent-gateway secret master rotate` generates a new key, re-encrypts every
secret row inside a single SQLite transaction, and only commits the new key
to storage after the re-encryption transaction succeeds. A crash mid-rotation
leaves the old key authoritative.

### Schema

All state lives in a single SQLite DB at
`~/.local/share/agent-gateway/state.db` (WAL mode, `0600`). Tables:

```sql
CREATE TABLE agents (
  name         TEXT PRIMARY KEY,
  token_hash   BLOB NOT NULL,              -- argon2id
  token_prefix TEXT NOT NULL,              -- first 8 chars of raw token, plaintext
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER,
  description  TEXT
);

CREATE TABLE secrets (
  id           TEXT PRIMARY KEY,           -- sec_<ulid>; stable across rotations
  name         TEXT NOT NULL,              -- referenced as ${secrets.<name>}
  agent        TEXT REFERENCES agents(name) ON DELETE CASCADE,
                                            -- NULL = global
  ciphertext   BLOB NOT NULL,              -- AES-256-GCM
  nonce        BLOB NOT NULL,              -- 12-byte per-row nonce
  created_at   INTEGER NOT NULL,
  rotated_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  description  TEXT,
  UNIQUE(name, agent)
);
CREATE INDEX idx_secrets_agent ON secrets(agent);

CREATE TABLE requests (
  id               INTEGER PRIMARY KEY,
  ts               INTEGER NOT NULL,
  agent            TEXT REFERENCES agents(name) ON DELETE SET NULL,
  method           TEXT NOT NULL,
  host             TEXT NOT NULL,
  path             TEXT NOT NULL,
  query            TEXT,
  status           INTEGER,                -- NULL on transport error
  duration_ms      INTEGER NOT NULL,
  bytes_in         INTEGER NOT NULL,
  bytes_out        INTEGER NOT NULL,
  matched_rule     TEXT,                   -- rule name; NULL = pass-through/tunnel
  verdict          TEXT,                   -- allow | deny | require-approval | pass-through | tunnel
  approval         TEXT,                   -- approved | denied | timed-out; NULL if n/a
  credential_ref   TEXT,                   -- "gh_bot"
  credential_scope TEXT,                   -- "global" or agent name
  credential_id    TEXT,                   -- stable secrets.id at time of use
  error            TEXT
);
CREATE INDEX idx_req_ts    ON requests(ts);
CREATE INDEX idx_req_agent ON requests(ts, agent);
CREATE INDEX idx_req_host  ON requests(ts, host);
CREATE INDEX idx_req_rule  ON requests(matched_rule, ts);
```

Foreign keys: secrets `ON DELETE CASCADE` (removing an agent scrubs its
scoped secrets); audit rows `ON DELETE SET NULL` (history survives agent
deletion for forensics).

Only `secrets.ciphertext` is encrypted at rest. Agents and audit tables
contain no secret values by design (names, hashes, prefixes only).
Filesystem `0600` is the at-rest protection for the DB file itself.

### Scope resolution

Most-specific-wins: agent-scoped beats global on the same name. Single SQL
query with deterministic ordering:

```sql
SELECT * FROM secrets
WHERE name = ?1
  AND (agent = ?2 OR agent IS NULL)
ORDER BY agent IS NULL ASC
LIMIT 1;
```

Not found → rule fails with a logged error; request falls through to
pass-through (dummy credential intact). Preserves the fail-safe.

### Shadow warnings

`agent-gateway secret set gh_bot --agent claude-review` warns if a global
`gh_bot` already exists. Reverse direction also warns. `secret list` surfaces
scope in its own column so shadows are visible at a glance.

### Audit differentiation

Each audit row records three fields for credential tracing:

- `credential_ref` — the name in the rule template (what was asked for).
- `credential_scope` — `"global"` or the agent name (what actually resolved).
- `credential_id` — the stable `secrets.id` used at that moment. Survives
  rotations; a delete+recreate issues a new id. Lets forensics answer "which
  requests used the secret I rotated last Tuesday?"

### CLI

```
agent-gateway secret set <name>                         # TTY prompt or stdin
agent-gateway secret set <name> --agent <agent> --from-file <path>
agent-gateway secret list                               # no values, ever
agent-gateway secret rotate <name> [--agent <agent>]
agent-gateway secret rm <name> [--agent <agent>]
agent-gateway secret master rotate
agent-gateway secret export <name> --confirm-insecure   # stdout; no logs
```

### Runtime hygiene

Decrypted values cached in-memory LRU keyed by `(agent, name)`, TTL
configurable (default 60s). Cache cleared on `secret rotate`, on
`secret master rotate`, and on rules reload. Plaintexts never written to any
log or audit row, never reflected through dashboard or HTTP. The only code
path that can touch plaintext is template expansion inside the request
pipeline.

## 6. TLS MITM Mechanics

### Root CA

Generated on first `serve`: P-256 ECDSA key, self-signed root, CN
`"agent-gateway local CA"`, valid 10 years. Persisted to
`~/.local/share/agent-gateway/ca.key` (`0600`) and `ca.pem` (`0644` — the
cert half is deliberately world-readable so sandboxes can fetch it).

The CA key is **not** stored in the OS keychain — it needs to be loadable on
every start before the keychain is unlocked, and `0600` filesystem protection
is equivalent for a single-user local tool.

`agent-gateway ca rotate` generates a fresh root atomically; documented as
disruptive (every sandbox must re-trust).

### Leaf issuance

On MITM'd CONNECT, `internal/proxy` looks the target hostname up in an
in-memory `sync.Map[string]*tls.Config`. Miss → issue a P-256 leaf with
SANs `[host]`, signed by the root, 24h validity, 1h refresh buffer. Cached
value is a fully-built `*tls.Config` so the hot path is allocation-free.
Background sweeper removes expired entries every 5 min.

### ALPN & HTTP/2

Leaf `tls.Config.NextProtos = []string{"h2", "http/1.1"}`. We negotiate
independently with the agent and with the upstream, and bridge h1↔h2 if
needed using Go's stdlib. Trailers, server-push, and CONNECT-over-h2 are out
of scope.

### CONNECT-time intercept decision

Hostname is known, path/headers/body are not. Rules loader maintains a
derived "any rule whose host glob matches X" lookup, refreshed on every
reload:

1. Exact or glob host match → MITM.
2. Host on `proxy_behavior.no_intercept_hosts` (pinned upstreams) → force
   pass-through, even if a rule would match.
3. No match → pure TCP tunnel (bytes-in/out + duration audited).

### Upstream TLS

When dialing upstream, we use the **system** trust store, not our own CA.
`tls.Config{InsecureSkipVerify: false, ServerName: host}`. We are a MITM to
the agent, but a strict TLS client to the upstream. Verification failures
become `502 Bad Gateway` to the agent with the specific error logged.

### Pinned clients

Some upstreams (certain desktop apps, older mobile SDKs) pin fingerprints and
will refuse our leaf. Configurable `no_intercept_hosts` forces pass-through
for named hosts. Clients we care about for agent workflows (`curl`, `gh`,
`git`, Node, Python, Go `net/http`) do not pin.

### Trust distribution

- `GET :8221/ca.pem` — serves root cert, unauthenticated. Public-key
  material by design.
- `agent-gateway ca export` — CLI writes PEM to stdout. Primary path for
  sandbox-manager integration.

No `/bootstrap.sh` endpoint (dropped from earlier draft — sandbox-manager
owns install scripting).

No `POST /agents` endpoint in v1 (deferred). Agent creation is CLI-only;
integrations shell out.

### Security properties

- Agents never see the root CA key.
- Agents never see real secrets for hosts they don't have rules for
  (pass-through preserves dummy credentials).
- CA cert is world-readable by design (public-key material).
- Leaf private keys live only in memory, never persisted.

## 7. Agent Identity

### Token format

`agw_` + 32 bytes of base62 entropy (~38 chars total). Chosen prefix makes
tokens visibly agent-gateway tokens in logs (à la `ghp_`, `sk-`).

Proxy-Authorization value is `Basic base64("x:" + full-token)`, onecli
convention.

### Persistence

Only the hash is stored (`argon2id`). Plaintext is printed once at
`agent add`; if lost, rotate. `token_prefix` (first 8 chars) is stored
plaintext for disambiguation in dashboard / CLI listings.

### Auth hot path

On every CONNECT: extract token, parse prefix, look up the `(prefix → [hash,
name])` entry in an in-memory map populated at startup and invalidated on
`agent add/rm/rotate`. One argon2id comparison per request — prefix filters
to a single candidate.

`last_seen_at` updates are coalesced in a background goroutine, flushed at
most once per 30s per agent. Keeps the hot path free of writes.

### CLI

```
agent-gateway agent add <name> [--description "..."]
  → prints token once + ready-to-paste HTTPS_PROXY URL

agent-gateway agent list
agent-gateway agent show <name>
agent-gateway agent rm <name>
agent-gateway agent rotate <name>           # no grace window; new token immediately
```

## 8. Dashboard & Audit Log

### Dashboard

Embedded SPA at `:8221/dashboard/`, vanilla JS + HTML, no build step (same
pattern as mcp-broker). Tabs:

- **Live feed** — SSE stream. Each row: timestamp, agent, `METHOD host/path`,
  verdict badge, matched rule (clickable → jump to rule file path),
  duration, status. Pulses amber for pending `require-approval` rows with
  inline approve/deny buttons.
- **Approvals** — filter of the feed showing only pending approvals, with
  full match context (which headers/body fields triggered the rule) so the
  approver can decide in one view.
- **Audit** — paginated, filterable history. Filters: agent, host,
  matched-rule, verdict, time range, free-text path. Rows expand to show
  the full audit record (no bodies, no values).
- **Rules** — rendered by file, with "last matched" and "match count (24h)"
  from the audit index. Read-only. A "test request" form runs the same
  matcher as `rules check --request`.
- **Agents** — list with last-seen, request-count-by-verdict (24h).
  Plaintext tokens never shown after `agent add`; only the 8-char prefix.
- **Secrets** — list by (name, scope, created, rotated, last-used,
  referencing-rule count). No values, no export.

SSE ring buffer of last 200 events on the server for refresh rehydration
without a DB round-trip.

### Admin auth

Single admin bearer token at `~/.config/agent-gateway/admin-token` (`0600`),
generated on first run, printed once to stdout. Presented via
`?token=<x>` on first load, then set as `HttpOnly; SameSite=Strict` cookie.
Rotatable via `agent-gateway token rotate admin`.

Protects every dashboard endpoint including writes (`/api/decide` for
approval). Admin tokens and agent tokens are cryptographically distinct and
live in different files/tables; no cross-use. Same role as mcp-broker's auth
token; renamed for clarity because agent-gateway also has per-agent tokens.

### Audit write path

One prepared INSERT per completed request (success or failure). Audit errors
are logged but never block the request pipeline (`_ = auditor.Record(...)`
pattern from mcp-broker).

Pass-through (non-MITM) tunnels get a row with `matched_rule=NULL`,
`verdict="tunnel"`, bytes/duration only — no method/path because we
couldn't see them. No invisible traffic.

### Retention

Configurable. Default: keep 90 days, prune daily at local `04:00`.
Background loop; idempotent.

## 9. Configuration & On-disk Layout

### Paths

```
~/.config/agent-gateway/
  config.hcl                  # ports, paths, timeouts
  rules.d/                    # *.hcl, lexical filename order
    00-deny.hcl
    10-github.hcl
    10-atlassian.hcl
    20-anthropic.hcl
  admin-token                 # 0600, printed once at first run
  master.key                  # 0600, ONLY if OS keychain unavailable

~/.local/share/agent-gateway/
  state.db                    # 0600; agents + secrets + requests, WAL
  state.db-wal
  state.db-shm
  ca.key                      # 0600; root CA private key
  ca.pem                      # 0644; root CA cert (public)
```

### `config.hcl`

```hcl
proxy {
  listen = "127.0.0.1:8220"
}

dashboard {
  listen       = "127.0.0.1:8221"
  open_browser = true
}

rules {
  dir = "~/.config/agent-gateway/rules.d"
}

secrets {
  cache_ttl = "60s"
}

audit {
  retention_days = 90
  prune_at       = "04:00"
  sse_ring_size  = 200
}

approval {
  timeout = "5m"
}

proxy_behavior {
  no_intercept_hosts = []
  max_body_buffer    = "1MiB"
}

log {
  level  = "info"                # debug | info | warn | error
  format = "text"                # text | json
}
```

`config refresh` re-reads defaults and preserves overrides (mcp-broker
pattern). `config edit` opens in `$EDITOR`. `config path` prints location.

### Startup sequence (`serve`)

1. Load + validate `config.hcl`. Fail fast on parse errors.
2. Open `state.db`, run idempotent migrations (`PRAGMA user_version` +
   numbered Go migration functions).
3. Load root CA (generate on first run).
4. Resolve master key: OS keychain, else `master.key` file, else generate +
   store.
5. Parse `rules.d/*.hcl`, validate template references. Fail startup on
   invalid rules.
6. Start `fsnotify` watcher on `rules.d/`.
7. Start background workers: audit prune, `last_seen_at` flush, secret cache
   sweep, leaf-cert cache sweep.
8. Bind proxy (`:8220`) and dashboard (`:8221`).
9. If `open_browser` and stdout is a TTY and no `--headless`, open
   `http://127.0.0.1:8221/dashboard?token=<admin>` once.
10. Log startup summary: admin URL (first run only), agent count, secret
    count, loaded-rule count, MITM-eligible host list.

### Shutdown

`SIGTERM`/`SIGINT` → stop accepting new connections, 30s grace for in-flight,
flush pending `last_seen_at`, close DB cleanly. `SIGHUP` is an explicit
rules+config reload trigger (defensive; fsnotify normally covers this).

### CLI surface

```
agent-gateway serve
agent-gateway config {path, edit, refresh}
agent-gateway rules check [--request '...']
agent-gateway agent {add, list, rm, rotate, show}
agent-gateway secret {set, list, rotate, rm, master rotate, export}
agent-gateway ca {export, rotate}
agent-gateway token rotate admin
```

Cobra-based. `serve` holds a PID file in the config dir and refuses to start
if one is live (second instance would contend on DB + ports).

## 10. Prior Art & Attribution

agent-gateway draws on two reference points with fundamentally different
relationships. The distinction matters for licensing posture and for how
we credit them.

### Relationship summary

- **mcp-broker** — sibling tool in this same monorepo, same author, same
  license. About 1500 LOC of transport-agnostic infrastructure ports
  directly with minimal change. This is internal code reuse, not
  third-party incorporation.
- **onecli** (<https://github.com/onecli/onecli>) — external Rust tool in
  the same problem space. We treat it strictly as **architectural
  inspiration and prior art**:
  - No code is copied. agent-gateway is a clean-room Go reimplementation.
  - No binary artifacts (CA tooling, Node SDK, Next.js dashboard) from
    onecli are redistributed, wrapped, or embedded.
  - Different language (Rust → Go), different storage backend
    (Postgres → SQLite), different dependency stack — independent
    implementation throughout.
  - The design _ideas_ we adopt (listed below) are taken as concepts, not
    as implementation details. Where our implementation borrows a specific
    numeric choice (e.g. 24h leaf validity, 5-minute approval timeout),
    those are commodity values in the broader proxy/MITM ecosystem and
    not unique to onecli.
  - When agent-gateway ships, the README and a `NOTICE` file in the tool
    directory will credit onecli as prior art in this problem space.
    There is no license-compliance obligation beyond this attribution
    because no code or assets cross over.

This is the same category of relationship any clean-room reimplementation
has with its predecessor in a problem space.

### From mcp-broker (code carried over)

~1500 LOC of transport-agnostic infrastructure ports near-verbatim:

- XDG-aware JSON/HCL config loader pattern.
- SQLite WAL audit module (extended schema).
- Bearer-token auth middleware with constant-time comparison and
  cookie-promotion.
- Embedded-HTML SPA with SSE event bus.
- Cobra-based CLI with `config {path,edit,refresh}` UX.
- testify mocks + e2e subprocess tests.

### From onecli (ideas, re-implemented)

Architectural concepts adopted; all code written fresh in Go:

- CA + leaf issuance model (10y CA, 24h leaf, 1h refresh buffer,
  per-hostname tls.Config cache).
- `Proxy-Authorization: Basic base64("x:<token>")` convention (itself a
  mimicry of GitHub-PAT URL auth — pre-existing ecosystem convention).
- Opt-in per-rule approval verdict with timeout.
- `GET /ca.pem` distribution endpoint.
- 60s in-memory resolution cache keyed by `(agent, host)`.

### Improvements over onecli, explicitly

Concrete places agent-gateway diverges and does more:

- Header + content-type-aware body matching (onecli matches path + method
  only).
- Per-request audit log (onecli's `AuditLog` is operator-action only).
- Match-and-swap model with pass-through for unmatched hosts (onecli MITMs
  everything).
- Rules-as-code HCL directory with hot reload (onecli is dashboard-edit
  only).
- OS-keychain-protected master key (onecli uses an env var).
- HTTP/2 ALPN end-to-end.
- Agent-scoped secrets with stable-id audit trail.

### Shipping-time documentation requirements

Tracked here so it isn't forgotten at release:

- `agent-gateway/README.md` — "Prior art" section linking to onecli with a
  one-paragraph note that it is the primary inspiration for this tool.
- `agent-gateway/NOTICE` — short attribution file naming onecli as prior
  art, confirming no code is incorporated. Not required by any license,
  but good-citizen practice for clean-room reimplementations.

## 11. Open Questions & Risks

- **Body buffering cap correctness.** `max_body_buffer = 1MiB` covers almost
  every API call but will auto-fail body matchers on large uploads. We
  should expose "body matcher bypassed due to size" prominently in the
  audit row so users don't silently lose rule coverage on large payloads.
- **HTTP/2 end-to-end streaming.** Go's stdlib handles h1↔h2 bridging, but
  long-lived streaming responses (SSE from upstream, Anthropic
  streaming-token output) need verification. The proxy must not buffer
  response bodies.
- **Keychain availability on headless Linux.** `go-keyring` requires a
  running Secret Service daemon. Fallback to `master.key` file is fine but
  users on CI-ish machines will hit this. The startup warning must be
  loud.
- **Rule template validation at reload time depends on the live secrets
  table.** If a user writes a rule referencing a secret they haven't created
  yet and hot-reload runs before `secret set`, the reload fails. Workable
  but surprising. Could add a `--defer-validation` flag on `secret set` or
  loosen reload validation to warnings. Revisit after real-world use.
- **Rule ordering across files.** Lexical filename order is predictable but
  opaque at the UI level. The dashboard should render "effective order" (the
  flat evaluated list) as well as per-file views so users can audit priority
  at a glance.
- **Pinned-client detection.** We have no way to detect pinning before the
  handshake failure. Config-driven `no_intercept_hosts` is the only mitigation.
  A future improvement could observe repeated TLS verify failures from the
  agent side and auto-propose a pass-through entry.

## 12. Milestones

Rough sequencing for implementation planning:

1. Core skeleton: binary, config loader, XDG paths, SQLite migrations, CLI
   scaffolding. Can `serve` and bind ports.
2. CA + MITM plumbing: root CA, leaf issuance, CONNECT handler, h1 + h2 ALPN.
   End-to-end test: agent → MITM → example.com → agent, no rules.
3. Rule engine: HCL loader, matcher, hot-reload. First rule matches and
   sets a header.
4. Secrets: SQLite + AES-GCM + keychain, CLI verbs, template expansion in
   inject. Real GitHub PAT substitution works end-to-end.
5. Audit log + dashboard live feed + approve/deny. SSE replay buffer.
6. Agents: token mint/auth, per-agent rule scoping, per-agent secret
   scoping, audit fields.
7. Polish: `rules check --request`, shadow warnings, dashboard search,
   retention pruning, startup summary, documentation.

## 13. Code Organization & Testing Patterns

### Interface boundaries

Each `internal/<pkg>` exports **one primary interface** (the port) and one or
more concrete implementations (the adapters). Other packages depend on the
interface, not the struct. Example shapes:

```go
// internal/rules
type Engine interface {
    Evaluate(ctx context.Context, req *Request) (*Verdict, error)
    Reload() error
}

// internal/secrets
type Store interface {
    Get(ctx context.Context, name, agent string) (string, *Metadata, error)
    Set(ctx context.Context, name, agent, value string) error
    Rotate(ctx context.Context, id string, newValue string) error
    // ... list / delete / master-rotate
}

// internal/audit
type Logger interface {
    Record(ctx context.Context, entry Entry) error
    Query(ctx context.Context, filter Filter) ([]Entry, error)
    Prune(ctx context.Context, before time.Time) (int, error)
}

// internal/agents
type Registry interface {
    Authenticate(ctx context.Context, token string) (*Agent, error)
    Add(ctx context.Context, name, description string) (token string, err error)
    // ...
}

// internal/approval
type Broker interface {
    Request(ctx context.Context, r *PendingRequest) (Decision, error)
    Pending() []*PendingRequest
    Decide(id string, decision Decision) error
}

// internal/ca
type Authority interface {
    RootPEM() []byte
    ServerConfig(host string) (*tls.Config, error)
}

// internal/inject
type Injector interface {
    Apply(req *http.Request, binding Binding) error
}
```

Constructors return the interface (`func NewEngine(...) Engine`).
`internal/proxy` holds struct fields typed as the interfaces — every
dependency can be swapped for a mock in tests, and nothing in `proxy` needs
to know whether rules came from HCL files or an in-memory fixture.

### Test layering

Three distinct layers, matching mcp-broker's convention:

- **Unit tests** (`*_test.go`, default build): per-package, mock dependencies
  via `testify/mock`. Table-driven for matchers, parsers, template expansion,
  glob/regex edge cases. Should run in under 5 seconds for the whole repo.
- **Integration tests** (`//go:build integration`): real SQLite on
  `t.TempDir()`, real filesystem, real CA generation, real HCL parsing.
  Exercise two-or-three packages together (e.g. rules + secrets + inject →
  verify a rule actually produces the right header). No network, no child
  processes.
- **E2E tests** (`//go:build e2e`): built binary spawned as subprocess, real
  proxy port, real dashboard port, a mock upstream `httptest.Server` (we are
  a strict TLS client to it, so its cert is added to a test trust bundle).
  A test HTTP client sets `HTTPS_PROXY` to the subprocess, trusts the test
  CA, and makes real requests. Covers both intercept and tunnel paths.

Test helpers live in `test/testutil/` — a `Stack` builder that wires
real-or-mock components with one call, and per-layer defaults. Patterned on
mcp-broker's `testStack`.

### Conventions

- Constructors take context-free dependencies; `context.Context` is the
  _first_ parameter on every method that does I/O, cancellation, or audit.
- No package-level singletons. No `init()` side effects. Everything is
  constructed in `cmd/agent-gateway/serve.go` and passed down.
- Errors wrapped with `fmt.Errorf("doing X: %w", err)`; sentinels
  (`var ErrNotFound = errors.New(...)`) at package boundaries where callers
  need to branch.
- Structured logging via `log/slog`; a `slog.Logger` is a constructor
  dependency, not a global. Request ID threaded through `context.Context`.
- No panic on the request path. Panics in background workers log + continue.
- Concurrency contracts documented on each interface: which methods are safe
  to call concurrently, which are not.
