# agent-gateway — Design

**Status:** shipped · **Author:** Avery Crespi

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
- HCL-authored rules loaded from a directory; picked up by the daemon on
  `agent-gateway rules reload`.
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
- **Duplicate-approval deduplication.** If an agent retries a
  `require-approval` request, each retry creates its own pending card. The
  global approval cap (§8) bounds the blast radius; folding N identical
  requests into one decision is additive and deferable.
- **Agent / admin-token rotation grace window.** `agent rotate` and
  `token rotate admin` invalidate the old token immediately. Coordinated
  rotation (mint → update sandbox env → restart agent) is a local-dev
  workflow; dual-accept can be added when a concrete need appears.
- **Upstream auth-error body peek.** onecli inspects the first 8 KB of
  upstream 4xx bodies for auth-failure keywords to emit structured errors
  (Google returns 400 for missing keys). Useful diagnostic, not core
  functionality. Note: onecli's implementation buffers the full body —
  if we adopt this, use a peek-and-rejoin-stream pattern instead.
- **SSE Last-Event-ID replay + close-on-slow backpressure.** v1 uses
  mcp-broker's drop-on-full pattern for the live feed; the paginated
  `/dashboard/api/audit` endpoint covers "what happened while I was away." Adding
  durable replay + slow-client eviction later is additive — ULIDs are
  already on `id:` frames, no protocol change needed.
- **Ambient SSE event types** (`rule-reload`, `secret-change`,
  `backlog-warning`). v1 ships only request-lifecycle events (`request`,
  `approval`, `approval-resolved`). Rule/secret edits come from the user so
  they can refresh the tab; audit-write failures log to stderr.
- **Stable `credential_id` across rotations.** v1 records `credential_ref` +
  `credential_scope` and answers "which requests used the pre-rotation
  version?" with a JOIN against `secrets.rotated_at` (see §5 audit
  differentiation). Approximate — can't tell rotate-in-place from
  delete-then-recreate — but accurate enough for v1.
- **Dashboard Approvals / Tunneled-hosts tabs, rich Audit filters,
  test-request form.** v1 ships five tabs (Live feed, Audit, Rules, Agents,
  Secrets). Pending approvals pin to the top of Live feed with inline
  approve/deny; tunneled-hosts discoverability surfaces as a Live-feed
  banner; Audit ships with time-range pagination only.
- **`rules check --request` / `--replay` CLI forms.** v1's `rules check`
  validates syntax only. The explicit-reload iteration loop
  (edit → `rules reload` → agent retries → see `matched_rule` update on
  live feed) covers v1 without synthetic-request testing.

### Success criteria

- Sandbox points `HTTPS_PROXY` at the gateway and trusts the gateway CA; a
  rule of the form "inject `${secrets.gh_bot}` for
  `POST api.github.com/repos/*/issues`" works against a real sandbox running
  `gh` without the sandbox ever seeing the real token.
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
                             secret {set,list,rotate,rm,master rotate},
                             rules {check,reload}, token rotate admin,
                             ca {export,rotate}, config {path,edit,refresh}

internal/proxy/         MITM HTTP/HTTPS proxy, CONNECT handler, per-host
                        *tls.Config cache, ALPN (h1 + h2), body buffering
internal/ca/            Root CA load/generate, leaf issuance (24h, 1h refresh buffer)
internal/rules/         HCL loader (directory glob), matcher, explicit
                        reload via SIGHUP, first-match-wins ordered evaluation
internal/inject/        Header verbs (replace_header, remove_header),
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
internal/store/         Single SQLite file, WAL mode, migrations
internal/daemon/        PID file write/read/delete for CLI→daemon SIGHUP
internal/paths/         XDG-conformant path helpers
internal/hostnorm/      Canonical host + host-glob normalization (IDNA
                        Lookup, case-fold, trailing-dot strip); used at
                        every rule/config/CONNECT ingress point
```

### Ports

- **`:8220` proxy** — HTTP CONNECT + plain HTTP. Bound `127.0.0.1` by default.
- **`:8221` dashboard** — dashboard SPA, `/ca.pem`, SSE. Bound `127.0.0.1` by
  default.

Both override-able via `config.hcl` and CLI flags. Ports chosen to be adjacent
(sibling-tool recognisable) and non-conflicting with `mcp-broker`'s `:8200`.

### Request lifecycle (HTTPS MITM path)

1. Agent opens TCP to `:8220`, sends `CONNECT api.github.com:443 HTTP/1.1`
   with `Proxy-Authorization: Basic base64("x:agw_…")`.
2. Proxy validates agent token → resolves agent name. The **MITM decision
   table in §6** is consulted (inputs: valid token, `no_intercept_hosts`, any
   agent-scoped rule matching the host, IP-literal target).
   **Tunnel path → pure TCP relay**; audit row has `interception='tunnel'`,
   bytes in/out + duration only (no method/path).
3. MITM path → 200 OK back to agent; handshake using a cached-or-issued leaf
   cert for `api.github.com` signed by our root CA; ALPN advertises
   `h2,http/1.1`.
4. Request decoded (h2 frames or h1). Matcher evaluates rules in filename
   order × within-file order → first match wins → rule verdict.
5. Rule verdict dispatch:
   - **allow**: apply `inject` block (replace_header / remove_header with
     `${secrets.X}` / `${agent.X}` expansion) → dial upstream using system
     trust store with strict TLS verification → stream request → stream
     response → audit with `injection='applied'`, `outcome='forwarded'`.
   - **deny**: synthesise `403 Forbidden`; audit with `outcome='blocked'`.
   - **require-approval**: park request in approval store, push SSE event to
     dashboard, block until decision or approval timeout. Approved → continue
     as allow. Denied → `403` (`outcome='blocked'`). Timed-out →
     `504 Gateway Timeout`.
6. Unmatched request on a MITM'd host: pass through untouched (dummy
   credential intact) → audit with `matched_rule=NULL`,
   `outcome='forwarded'`. This is the fail-safe: forgotten routes fail
   upstream as unauthenticated rather than leaking real credentials.
7. Rule matched but credential resolution failed (secret missing, or exists
   but not under a scope the caller can access): same network behaviour as
   (6), but audited as `injection='failed'` with `error='secret_unresolved'`.
   Dashboard renders a badge on the rule so broken references are
   discoverable. (v1 collapses "missing" and "scope-excluded" into one value
   — splitting them back apart later is additive.)

### Agent-to-gateway authentication

Agent token travels in the proxy URL's userinfo, not in an explicit header:

```bash
export HTTPS_PROXY=http://x:agw_a1b2c3…@host.internal:8220
export HTTP_PROXY=http://x:agw_a1b2c3…@host.internal:8220
```

Every well-behaved HTTP client (`gh`, `curl`, `git`, Node, Python-requests,
Go `net/http`) converts this into `Proxy-Authorization: Basic base64("x:agw_…")`
on CONNECT. The `x:` is an arbitrary placeholder username (HTTP clients won't
accept a URL with a password but no username); onecli uses the same
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

### CLI / daemon coordination

State-mutating CLI commands (`secret set/rotate/rm`, `agent add/rm/rotate`,
`token rotate admin`, `ca rotate`, `config refresh`, `rules reload`) apply
the change (write SQLite for state, or just re-read files for `rules reload`)
and then signal the running daemon to reload. Specifically:

1. CLI opens the DB with `busy_timeout=5s` (covers transient contention with
   the daemon's audit writer under WAL). `rules reload` skips this step — it
   touches no DB state.
2. On successful write, CLI reads the PID file and sends `SIGHUP` to the
   daemon. If the daemon isn't running, the CLI is a no-op beyond the DB
   write — the daemon picks up the new state on next start.
3. Daemon's `SIGHUP` handler triggers a **coarse reload**: re-parse
   `config.hcl`, re-parse `rules.d/`, rebuild the agent prefix→hash map,
   invalidate the decrypted-secret LRU. Request handlers continue to read
   from the pre-reload `atomic.Pointer` snapshot until the swap completes.
4. Before signalling, CLI verifies the process at that PID is actually
   `agent-gateway` by reading `/proc/<pid>/comm` (Linux) or
   `ps -p <pid> -o comm=` (macOS). PID reuse on a local-dev machine is rare
   but SIGHUP to, say, a user's editor would be disruptive — the comm-name
   check is one syscall and catches the race cleanly without extra state to
   keep in sync.

SIGHUP is _the_ reload mechanism — no auxiliary filesystem watcher. Rule
edits are picked up via explicit `agent-gateway rules reload`; this keeps the
reload path single, avoids half-written-file races from editor saves, and
fits the same pattern as `secret set` and friends (every state change has a
corresponding CLI command). Users who want the tighter loop can wrap their
editor or run `entr -r` externally.

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
  agents = ["claude-review", "codex-sandbox"]  // optional; omit = all agents

  match {
    host   = "api.github.com"              // glob: * within segment, ** multi-segment
    method = "POST"                        // optional; default = any
    path   = "/repos/*/*/issues"

    headers = {                            // attribute map: name -> regex (RE2); AND
      "X-GitHub-Api-Version" = "^2022-"
    }

    json_body {                            // labeled block; implies Content-Type: application/json
      jsonpath "$.title"     { matches = "^\\[bot\\]" }
      jsonpath "$.labels[*]" { matches = "^automation$" }
    }
  }

  verdict = "allow"                        // allow | deny | require-approval

  inject {                                 // only for allow / require-approval
    replace_header = { "Authorization" = "Bearer ${secrets.gh_bot}" }
    remove_header  = ["X-Agent-Hint"]
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

A rule with a `json_body`, `form_body`, or `text_body` block matches only
requests that both (a) carry a body and (b) have a `Content-Type` matching
the declared block type. Requests without a body — including `GET`, `DELETE`,
`HEAD`, and `POST`/`PUT` with `Content-Length: 0` — never match a
body-matcher rule. No silent coercion (e.g. empty body is not treated as
empty JSON). Out of scope for v1: multipart, protobuf, gRPC.

### Matching semantics

- `host`, `path`: globs, `*` within a segment, `**` across segments. Compiled
  at load time.
- `method`: exact, uppercase.
- `headers`, `json_body`/`form_body`/`text_body` matchers: Go `regexp` (RE2).
  All declared matchers must succeed (AND).
- Body matchers require buffering the request body up to
  `proxy_behavior.max_body_buffer` (default `1MiB`); beyond the cap, the
  body cannot be evaluated and the request is **blocked with 403**
  (fail-closed) regardless of the rule's verdict. The audit row records
  `error='body_matcher_bypassed:size'`, the bypassed rule name, and the
  rule's intended verdict.
- Body buffering is additionally bounded by `timeouts.body_buffer_read`
  (§9); exceeding the wall clock triggers
  `error='body_matcher_bypassed:timeout'` with the same fail-closed
  semantics. Treating a bypass as fail-closed prevents an agent from
  evading a `deny` rule by padding the body past the cap, and prevents
  an `allow` rule's narrowing condition from being silently skipped.

### Authoring conventions

Two HCL shapes are used, chosen by what the construct needs to express:

- **Attribute maps** (`headers = { ... }`, `replace_header = { ... }`) for
  simple name → value mappings. Concise for the common "match a header's
  pattern" / "replace a header's value" case.
- **Labeled blocks** (`jsonpath "$.x" { ... }`, `field "name" { ... }`) for
  constructs that associate a path/field with multiple attributes (e.g. a
  matcher with `matches`). Labelled blocks are extensible — future attributes
  can be added without breaking existing rules.

Rule authors don't choose between the two; each matcher has a fixed shape.

### `agents` attribute

- Omitted → rule applies to all agents (default).
- Non-empty list → rule applies only to listed agent names.
- `agents = []` (empty list) → **load-time error.** Use rule deletion to
  disable a rule; don't express "applies to no agents" as an empty list.

### Verdict precedence

First-match-wins across filename order then within-file order. No separate
"pass evaluation" sorting by verdict type. If ordering matters, express it
explicitly via filename prefixes.

### Template expansion

Only at injection time. Variables:

- `${secrets.<name>}` — resolved against the secrets table at request time.
- `${agent.name}` — the calling agent's name.

**Secret values are interpolated as opaque bytes.** No re-expansion, no
escaping, no recursive template resolution. A secret containing `${x}` or
backslashes is inserted literally.

**Two-phase validation:**

- **Load time (strict):** template syntax only — does the template parse,
  are variable names well-formed (`secrets.<identifier>` or
  `agent.<field>`)? An invalid reload is rejected; the previous valid
  rule-set stays live.
- **Request time (lazy):** the referenced secret either resolves or doesn't.
  Any unresolved reference (missing entirely, or exists but only under a
  scope the caller can't access) → the rule fails soft: dummy credentials go
  upstream untouched and the audit row records `injection='failed'`,
  `error='secret_unresolved'`. The `pass-through` fail-safe is preserved.

This split lets a user write a rule that references a secret before creating
the secret, delete a secret that's still referenced, or temporarily remove
an agent's scope — all without breaking the running daemon.
`agent-gateway rules check` surfaces unresolved references as warnings (not
errors); the dashboard renders a "missing secret" badge on affected rules.

### Injection verbs

Two in v1:

- `replace_header` — create-or-overwrite. `{ "Name" = "value" }`. Covers the
  common "strip the dummy, set the real one" case in one verb: the header is
  unconditionally overwritten whether it was present on the incoming request
  or not.
- `remove_header` — `["Name1", "Name2"]`. For the strip-only case where no
  replacement is desired.

onecli's set-only-if-present verb is redundant because the same condition
can be expressed in the `match` block:

```hcl
match {
  headers = { "Authorization" = "^Bearer " }   // match only requests that DID auth
}
inject {
  replace_header = { "Authorization" = "Bearer ${secrets.gh_bot}" }
}
```

This keeps intent visible in code review.

### Reload

Rule changes are picked up via `agent-gateway rules reload`, which sends
`SIGHUP` to the daemon (see §3 CLI / daemon coordination). On `SIGHUP`:

1. Re-parse the whole `rules.d/` directory.
2. Validate HCL syntax, glob/regex compilation, and template syntax
   (existence of referenced secrets/agents is NOT checked — that is
   request-time lazy; see Template expansion above).
3. On success: swap the `atomic.Pointer[ruleset]`. In-flight requests finish
   on the old set; new requests use the new set.
4. On failure: log error to stderr; keep previous rule-set live.

`SIGHUP` also re-reads `config.hcl`, rebuilds agent/secret caches,
invalidates the decrypted-secret LRU, and reloads the root CA (re-reads
`ca.key`/`ca.pem` from disk and clears the leaf-cert cache so subsequent
TLS handshakes are signed under the rotated root) — a single coarse
reload path for all state.

### CLI

```
agent-gateway rules check     # syntax validation; exits non-zero on errors
agent-gateway rules reload    # sends SIGHUP after the comm-name check
```

## 5. Secrets

### Master key

The master key is versioned. The currently active id is stored in the
SQLite `meta` table (`key='active_key_id'`, value parsed as an integer);
the corresponding key bytes are stored in the OS keychain
(`go-keyring`; service `agent-gateway`, account `master-key-<id>`) with a
file fallback at `~/.config/agent-gateway/master-key-<id>` (mode `0600`,
written via the `internal/atomicfile` helper). Files are only present when
the keychain is unavailable; a prominent startup warning logs the fallback.

On first run `internal/secrets` reads `meta.active_key_id` (seeded to `1`
by migration 6), and `ResolveID(1, ...)` either finds an existing key,
migrates a pre-versioning `master-key` account / `master.key` file to the
id=1 location, or generates and persists a new key.

`agent-gateway secret master rotate` performs a crash-safe rotation:

1. Generate a new key, persist it under id `active+1` BEFORE opening any
   transaction.
2. In a single SQLite transaction: re-encrypt every secret row with the
   new key, then `UPDATE meta SET value = '<new id>' WHERE key =
   'active_key_id'`, then commit.
3. Best-effort delete the previous id's key from keychain and disk.

A crash before commit leaves `meta.active_key_id` pointing at the old id
(rollback) and the orphaned new key is removed by a deferred cleanup. A
crash after commit but before step 3 leaves `active_key_id` pointing at
the new id, which decrypts every row; the previous key becomes a harmless
orphan. In every case the persisted `active_key_id` names a key that can
decrypt every persisted row.

### Schema

All state lives in a single SQLite DB at
`~/.local/share/agent-gateway/state.db` (WAL mode, `0600`). Tables:

```sql
CREATE TABLE agents (
  name         TEXT PRIMARY KEY,
  token_hash   BLOB NOT NULL,              -- argon2id
  token_prefix TEXT NOT NULL,              -- first 12 chars of raw token ("agw_" + 8 body chars), plaintext
  argon2_salt  BLOB NOT NULL,              -- 16-byte per-row salt
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER,
  description  TEXT
);

CREATE TABLE secrets (
  id           INTEGER PRIMARY KEY,        -- SQLite rowid; no external meaning
  name         TEXT NOT NULL,              -- referenced as ${secrets.<name>}
  scope        TEXT NOT NULL,              -- 'global' | 'agent:<name>' | (future: 'group:...', etc.)
  ciphertext   BLOB NOT NULL,              -- AES-256-GCM
  nonce        BLOB NOT NULL,              -- 12-byte per-row nonce
  created_at   INTEGER NOT NULL,
  rotated_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  description  TEXT,
  UNIQUE(name, scope)
);
CREATE INDEX idx_secrets_scope ON secrets(scope);

CREATE TABLE requests (
  id               TEXT PRIMARY KEY,        -- ULID assigned at request decode
  ts               INTEGER NOT NULL,
  agent            TEXT,                   -- agent name at request time; no FK (see migration 5)
  interception     TEXT NOT NULL,          -- tunnel | mitm
  method           TEXT,                   -- NULL for tunnel rows
  host             TEXT NOT NULL,
  path             TEXT,                   -- NULL for tunnel rows
  query            TEXT,
  status           INTEGER,                -- NULL on transport error
  duration_ms      INTEGER NOT NULL,
  bytes_in         INTEGER NOT NULL,
  bytes_out        INTEGER NOT NULL,
  matched_rule     TEXT,                   -- rule name; NULL if no match
  rule_verdict     TEXT,                   -- allow | deny | require-approval
  approval         TEXT,                   -- approved | denied | timed-out
  injection        TEXT,                   -- applied | failed
  outcome          TEXT NOT NULL,          -- forwarded | blocked
  credential_ref   TEXT,                   -- "gh_bot"
  credential_scope TEXT,                   -- 'global' | 'agent:<name>'
  error            TEXT                    -- structured tag
);
CREATE INDEX idx_req_ts    ON requests(ts);
CREATE INDEX idx_req_agent ON requests(ts, agent);
CREATE INDEX idx_req_host  ON requests(ts, host);
CREATE INDEX idx_req_rule  ON requests(matched_rule, ts);
```

Foreign keys: `requests.agent` has no FK (migration 5 dropped it); audit rows
retain the agent name at request time even after the agent is deleted, so
history survives for forensics. `secrets.scope` has no FK either; when an
agent is removed, `agent rm <name>` issues a transactional cascade in code:
`DELETE FROM secrets WHERE scope = 'agent:'||?; DELETE FROM agents WHERE name = ?;`
This is grep-able and keeps "what happens when I delete an agent" visible at
the call site rather than hidden in schema DDL.

Only `secrets.ciphertext` is encrypted at rest. Agents and audit tables
contain no secret values by design (names, hashes, prefixes only). Filesystem
`0600` is the at-rest protection for the DB file itself.

### Audit columns: the request story

Five orthogonal columns describe what happened to each request. Each answers
exactly one question, so queries and dashboard filters stay clean.

| Column         | Values                                           | Question it answers          |
| -------------- | ------------------------------------------------ | ---------------------------- |
| `interception` | `tunnel` \| `mitm`                               | Did we decrypt TLS?          |
| `matched_rule` | rule name, or NULL                               | Did any rule match?          |
| `rule_verdict` | `allow` \| `deny` \| `require-approval`, or NULL | What did the rule declare?   |
| `approval`     | `approved` \| `denied` \| `timed-out`, or NULL   | What did the approver say?   |
| `injection`    | `applied` \| `failed`, or NULL                   | Did real credentials go out? |
| `outcome`      | `forwarded` \| `blocked`                         | Did the agent get its bytes? |

Representative rows:

| interception | matched_rule     | rule_verdict     | approval  | injection | outcome   | notes                               |
| ------------ | ---------------- | ---------------- | --------- | --------- | --------- | ----------------------------------- |
| tunnel       | NULL             | NULL             | NULL      | NULL      | forwarded | host had no rule; no MITM           |
| mitm         | NULL             | NULL             | NULL      | NULL      | forwarded | MITM'd host, no rule matched path   |
| mitm         | github-issues    | allow            | NULL      | applied   | forwarded | happy path                          |
| mitm         | github-issues    | allow            | NULL      | failed    | forwarded | secret missing; dummy went upstream |
| mitm         | prod-deploy      | require-approval | approved  | applied   | forwarded | human-in-the-loop approved          |
| mitm         | prod-deploy      | require-approval | denied    | NULL      | blocked   | human-in-the-loop denied            |
| mitm         | prod-deploy      | require-approval | timed-out | NULL      | blocked   | 504 to agent                        |
| mitm         | block-all-delete | deny             | NULL      | NULL      | blocked   | deny rule                           |

**Forensic queries** become straightforward:

- "Which requests got real credentials?" → `WHERE injection = 'applied'`.
- "What's been blocked?" → `WHERE outcome = 'blocked'`.
- "Broken rules (matched but couldn't inject)" →
  `WHERE injection = 'failed'`.
- "Blind traffic (no visibility)" → `WHERE interception = 'tunnel'`.

The invariant `credential_ref IS NOT NULL ⟺ injection = 'applied'` holds —
code-enforced, not DB-enforced.

### Scope format

Scope values are always prefixed or the literal `'global'`:

- `'global'` — applies to any agent.
- `'agent:<name>'` — applies only to the named agent.
- Future types (deferred): `'group:<name>'` for agent groups, etc.

Prefixing leaves room for additional scope types without reserving agent
names or migrating the column. Parsing is a simple prefix check
(`scope == 'global'` vs `strings.HasPrefix(scope, "agent:")`).

### Scope resolution

Most-specific-wins: agent-scoped beats global on the same name. Single SQL
query with deterministic ordering:

```sql
SELECT * FROM secrets
WHERE name = ?1 AND scope IN ('global', 'agent:' || ?2)
ORDER BY scope = 'global' ASC
LIMIT 1;
```

`?2` is the calling agent's name; the caller prepends the `agent:` prefix at
the storage boundary so the rest of the code works with already-typed
values.

Resolution outcomes:

- **Resolved and in scope:** proceed with injection; audit `injection='applied'`,
  `credential_scope` set to the matched scope value (`'global'` or
  `'agent:<name>'`).
- **Host-scope violation:** the secret resolved, but its `allowed_hosts`
  does not cover the request's target host. Rule fails **hard**: the gateway
  synthesises a `403 Forbidden` and audits `injection='failed'`,
  `error='secret_host_scope_violation'`. The request is not forwarded —
  forwarding with the dummy credential would mask the misconfig as an
  upstream 401.
- **Unresolved:** either no row matches (no global, no caller-scoped) or a
  row exists only under a different agent's scope. Rule fails soft; audit
  `injection='failed'`, `error='secret_unresolved'`. Dummy credential goes
  upstream.

The fail-safe is preserved either way: no real credential ever substitutes a
dummy for a request the gateway can't confidently authorise.

#### Host-scope binding

Every row in the `secrets` table carries a non-empty `allowed_hosts` JSON
array (migration 7) of normalised host globs — same semantics as rule
`match.host`. Bindings are created at `secret set` time via one or more
`--host` flags; later adjustments go through `secret bind` / `secret
unbind`. `secret unbind` refuses to leave a secret with an empty list —
the only way to "un-bind everything" is `secret rm`, which forces the
operator to acknowledge that the credential is being decommissioned.

The runtime check lives in `internal/inject`: `Expand` (and `Injector.Apply`
by extension) takes a `host` argument and calls
`secrets.HostScopeAllows(allowedHosts, host)` before substituting the
expanded value into the header. A mismatch returns
`inject.ErrSecretHostScopeViolation`, which the proxy pipeline translates
to the 403 described above. Rule load-time in `cmd/agent-gateway`'s
`warnSecretCoverage` helper warns when a rule references a secret whose
`allowed_hosts` does not obviously cover the rule's `match.host`; the
check is approximate (glob-pattern subset is non-trivial) but catches the
common "bound to wrong host" misconfig before first request.

### Shadow warnings

`agent-gateway secret set gh_bot --agent claude-review` (stored as
`scope='agent:claude-review'`) warns if a `gh_bot` with `scope='global'`
already exists. Reverse direction also warns. `secret list` surfaces scope
in its own column so shadows are visible at a glance.

### Audit differentiation

Each audit row records two fields for credential tracing:

- `credential_ref` — the name in the rule template (what was asked for).
- `credential_scope` — `'global'` or `'agent:<name>'` (what actually resolved).

"Which requests used the pre-rotation version of gh_bot?" is answered
approximately with a JOIN against `secrets.rotated_at`:

```sql
SELECT r.* FROM requests r
JOIN secrets s
  ON s.name = r.credential_ref AND s.scope = r.credential_scope
WHERE s.name = 'gh_bot' AND s.scope = 'global'
  AND r.ts < s.rotated_at;
```

This is approximate — it can't tell a rotate-in-place from a rm-then-set
with the same name. That ambiguity is acceptable for v1; the right advice is
"use `secret rotate`, not delete-then-set." A stable `credential_id` that
survives rotations is deferred to v1.1 (see §2 non-goals).

### CLI

```
agent-gateway secret set <name> [--agent <agent>]       # value read from stdin
agent-gateway secret list                               # no values, ever
agent-gateway secret rotate <name> [--agent <agent>]
agent-gateway secret rm <name> [--agent <agent>]
agent-gateway secret master rotate
```

Plaintext read-out is intentionally not exposed through the CLI. The only
code path that can touch a plaintext secret is template expansion inside
the request pipeline; bulk export via the CLI would create a shell-history
disclosure vector with no legitimate in-product use case.

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
every start before the keychain is unlocked, and `0600` filesystem
protection is equivalent for a single-user local tool.

`agent-gateway ca rotate` generates a fresh root atomically (see
`internal/atomicfile`) and signals the running daemon via `SIGHUP`. The
daemon's reload handler calls `Authority.Reload`, which re-reads
`ca.key`/`ca.pem` from disk into a fresh `rootBundle`, swaps the
`atomic.Pointer` on the in-memory `Authority`, and clears the leaf-cert
cache so subsequent TLS handshakes are signed under the new root.
In-flight handshakes that already hold an old leaf complete normally.
Documented as disruptive (every sandbox must re-trust).

### Leaf issuance

On MITM'd CONNECT, `internal/proxy` looks the target hostname up in an
in-memory `sync.Map[string]*tls.Config`. Miss → issue a P-256 leaf with SANs
`[host]`, signed by the root, 24h validity, 1h refresh buffer. Cached value
is a fully-built `*tls.Config` so the hot path is allocation-free. Background
sweeper removes expired entries every 5 min.

### ALPN & HTTP/2

**Agent-facing:** leaf `tls.Config.NextProtos = []string{"h2", "http/1.1"}`
so modern clients negotiate h2 when they prefer it.

**Upstream-facing:** `http.Transport{ForceAttemptHTTP2: true}` with a system
trust store. The transport negotiates h2 or h1 per upstream, and Go's stdlib
handles h1↔h2 bridging transparently for request/response body bytes.
Trailers, server-push, and CONNECT-over-h2 are out of scope for v1.

**Streaming correctness:** response bodies are forwarded via the standard
`io.Copy` + `http.Flusher` pattern. No buffering. SSE and streaming-token
responses reach the agent chunk-by-chunk as upstream emits them. This is
explicitly required for Anthropic streaming output and is verified by the
e2e test suite (§13 milestone 2).

### SNI vs CONNECT target (trust boundary)

The leaf cert we issue is bound to the **CONNECT line's hostname**, not to
the TLS SNI the agent subsequently sends. If an agent CONNECTs to
`api.github.com:443` and then sends `evil.com` as SNI in the TLS handshake,
the leaf cert for `api.github.com` will fail TLS name verification on the
agent side. This is correct: the agent cannot claim one host at the proxy
layer and connect as a different host at the TLS layer.

Modern clients always send SNI matching the CONNECT target. The mismatch
case is either a misbehaving client (fails fast and loudly — good) or an
exfiltration attempt (fails for the same reason — also good).

### CONNECT-time intercept decision (normative)

At CONNECT time the gateway decides between **tunnel**, **MITM**, and
**reject**. The `Proxy-Authorization` header is the standard one set by HTTP
clients when `HTTPS_PROXY=http://x:<token>@host:port` — see §3
"Agent-to-gateway authentication" for the convention and §7 for token format.

Four inputs:

1. Does the CONNECT carry a valid `Proxy-Authorization` for a known agent?
2. Is the target host in `proxy_behavior.no_intercept_hosts`?
3. Does any currently-loaded rule have a `host` glob matching the target
   **and** apply to this agent (either `agents` omitted, or listing this
   agent's name)?
4. Is the target host an IP literal (v4 or v6)?

| Valid token | In `no_intercept_hosts` | Rule matches (agent-scoped) | IP literal | Decision                                         |
| ----------- | ----------------------- | --------------------------- | ---------- | ------------------------------------------------ |
| no          | —                       | —                           | —          | **reject** (`407 Proxy Authentication Required`) |
| yes         | yes                     | —                           | —          | **tunnel**                                       |
| yes         | no                      | no                          | —          | **tunnel**                                       |
| yes         | no                      | yes                         | yes        | **tunnel** (globs are hostname-only)             |
| yes         | no                      | yes                         | no         | **MITM**                                         |

The derived lookup table `host → {agent-names-with-rule | "all"}` is rebuilt
on every rules reload. Filtering by the calling agent at CONNECT avoids
wasted TLS handshakes for hosts the agent has no rule for.

**MITM path:** complete TLS with the agent using a leaf cert for the CONNECT
hostname, decode the request (h1 or h2), evaluate rules, dispatch per
verdict. Every request gets a full audit row (`interception='mitm'`,
method/path/etc. populated).

**Tunnel path:** proxy raw TCP bytes via `io.Copy`. Never decrypts TLS.
Audit row records `interception='tunnel'`, `host`, bytes/duration — no
method/path (we couldn't see them).

**Reload interaction:** CONNECT-time decisions commit for the lifetime of
the TLS session. Rule edits mid-session don't retroactively change
tunnel/MITM — they apply to subsequent CONNECTs. Per-request rule evaluation
inside a MITM'd session does use the latest snapshot
(`atomic.Pointer[ruleset]`), so adding an `allow` rule during an open MITM
session takes effect on the next request over that session.

**Implications for rule authors:**

- `agents = ["codex"]` is invisible to agent `claude` — `claude`'s traffic
  to that host tunnels.
- `agents` omitted → rule applies to all agents → MITM for any caller.
- The dashboard's "tunneled hosts (24h)" view (§8) surfaces hosts the agent
  is talking to that no rule covers. This is the primary discoverability aid
  for rule authoring.

### Upstream TLS

When dialing upstream, we use the **system** trust store, not our own CA.
`tls.Config{InsecureSkipVerify: false, ServerName: host}`. We are a MITM to
the agent, but a strict TLS client to the upstream. Verification failures
become `502 Bad Gateway` to the agent with the specific error logged.

### Pinned clients

Some upstreams (certain desktop apps, older mobile SDKs) pin fingerprints
and will refuse our leaf. Configurable `no_intercept_hosts` forces
pass-through for named hosts. Clients we care about for agent workflows
(`curl`, `gh`, `git`, Node, Python, Go `net/http`) do not pin.

`no_intercept_hosts` entries are validated at config load by
`internal/config.validateNoInterceptHosts`. Any pattern composed entirely
of `*` and `.` characters (e.g. `**`, `*`, `*.*`, `**.**`) is rejected —
the check sits before rule evaluation in `Decide`, so accepting a global
wildcard would silently disable every rule, audit row, and injection.
Real entries always have literal text in them; the validator rejects the
nuke-everything family explicitly so a misconfiguration can't reach a
running daemon. Both `Load` (startup) and `Save` (CLI write paths) call
the validator, so an invalid config never lands on disk via the CLI
either.

The validator also soft-warns on entries that, after stripping leading
wildcard labels, resolve to an ICANN-managed public suffix (e.g.
`*.com`, `**.co.uk`, `com`). Such an entry would tunnel every host under
a registry-controlled TLD past MITM — almost always a typo, but
permitted in case an operator genuinely intends it. The warning surfaces
at daemon startup (config is not re-read on `SIGHUP`, so config changes
require a restart to re-check).

### Trust distribution

- `GET :8221/ca.pem` — serves root cert, unauthenticated. Public-key
  material by design.
- `agent-gateway ca export` — CLI writes PEM to stdout. Primary path for
  sandbox-manager integration.

### Security properties

- Agents never see the root CA key.
- Agents never see real secrets for hosts they don't have rules for
  (pass-through preserves dummy credentials).
- CA cert is world-readable by design (public-key material).
- Leaf private keys live only in memory, never persisted.

## 7. Agent Identity

### Token format

`agw_` + 32 random bytes encoded as base62 (47 chars total: `agw_` + 43
base62 chars, zero-padded). The prefix makes tokens visibly agent-gateway
tokens in logs (à la `ghp_`, `sk-`).

Proxy-Authorization value is `Basic base64("x:" + full-token)`, onecli
convention.

### Persistence

Only the hash is stored (`argon2id`, with a per-row 16-byte salt).
Plaintext is printed once at `agent add`; if lost, rotate. `token_prefix`
(first 12 chars: `agw_` + 8 body chars) is stored plaintext for
disambiguation in dashboard / CLI listings and for O(1) auth lookup.

### Auth hot path

On every CONNECT: extract token, parse prefix, look up the
`(prefix → [hash, name])` entry in an in-memory map populated at startup and
invalidated on `agent add/rm/rotate`. One argon2id comparison per request —
prefix filters to a single candidate.

`last_seen_at` is written directly on every successful CONNECT — one SQL
UPDATE per auth. At single-user scale this is a non-issue; coalescing
(background goroutine, 30s-per-agent debounce) is deferrable to v1.1 if
profiling ever shows write contention.

### Request identity

Every request gets a ULID assigned at request decode — immediately after the
MITM TLS handshake completes (or immediately after CONNECT for tunnel rows),
before rule evaluation. This is the `requests.id` TEXT PRIMARY KEY.
Assigning it upfront (rather than at audit-insert time) means:

- The ID exists during the request lifetime, so it can be threaded through
  `context.Context` and appear in every `slog` attribute from decode onward.
- Gateway-synthesised responses (403, 502, 504, including the 504 that fires
  when a parked `require-approval` request times out) can carry
  `X-Request-ID: <ulid>` before any audit row is written. Agents that log
  `X-Request-ID` on failure can correlate directly with audit rows.
- The in-memory approval store and SSE event stream can reference the ULID
  without a DB roundtrip; the audit INSERT at request completion uses the
  same ID.

ULIDs are 26-char lexicographically sortable, so SSE `id:` frames and the
`WHERE id > :since ORDER BY id ASC` replay query in §8 work unchanged.

`X-Request-ID` is **not** surfaced on forwarded responses. We don't rewrite
upstream responses; the upstream's own correlation ID (if any) is preserved.

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
pattern as mcp-broker). Five tabs in v1:

- **Live feed** — SSE stream plus a pinned "pending approvals" section at
  the top. Each feed row: timestamp, agent, `METHOD host/path`,
  interception/outcome badges, matched rule (clickable → jump to rule file
  path), duration, status. Tunnel rows (no method/path) render dimmer and
  are collapsible behind a toggle. Pending-approval rows pin to the top of
  the feed with a distinctive background and inline approve/deny buttons;
  on resolution they animate down into the stream. A pending-count pill in
  the header jumps to the pinned section when clicked. **Body contents are
  never displayed on pending rows** — see "Approval view invariant" below.
  Initial page load pulls the last 200 rows via `/dashboard/api/audit` so the feed
  has history before the first SSE event arrives.
- **Audit** — paginated history, time-range only in v1. Rows render the
  full audit record (metadata only; no bodies, no values). Rich filters are
  deferred to v1.1 — for v1 the corresponding SQL is runnable directly
  against `state.db` via the CLI if forensics needs it.
- **Rules** — rendered by file, read-only. "Last matched at" and
  "match count (24h)" per rule from the audit index. Rules with zero
  matches in 24h show a subtle "never matched" indicator; rules with
  unresolved `${secrets.*}` references show a "missing secret" badge.
- **Agents** — list with last-seen, request-count-by-outcome (24h).
  Plaintext tokens never shown after `agent add`; only the 8-char prefix.
- **Secrets** — list by (name, scope, created, rotated, last-used,
  referencing-rule count). No values, no export.

### Approval view invariant

A pending-approval row (pinned at the top of Live feed in v1) shows:

- Agent name, matched rule name, request method, host, path, query.
- The per-agent pending count beside the agent name (typically 1; higher
  numbers indicate a retry loop).

The row does **not** show:

- Request or response body contents.
- Header values beyond the matched-rule's own assertions.
- Any secret material (the request carries dummy credentials because
  injection has not happened yet).

Rationale: approvers decide based on agent identity + matched-rule
semantics, not request payload. If a class of requests needs content-based
disambiguation, the rule author should narrow the `match` block until
matching = intent. This invariant eliminates the "did the dashboard leak
something" risk class entirely.

This is test-enforced — a dedicated e2e (§13 milestone 5) fires a request
with a distinctive body + non-asserted headers through a `require-approval`
rule and asserts neither surfaces on the SSE `approval` event or the
`/dashboard/api/pending` response.

### Approval queue limits

Pending approvals are bounded by a single global cap
(`approval.max_pending`, default 50). When the cap is hit, new
`require-approval` requests are rejected synchronously with `403 Forbidden`

- `Retry-After: 30`, body `{"error":"approval_queue_full"}`. The rejection
  is audited (`outcome='blocked'`, `error='queue_full'`).

Queue pressure is visible on the live feed as the pending-count pill in the
dashboard header. A separate 90%-banner warning + per-agent caps +
overflow-behaviour variants are deferred to v1.1 — single-user local-dev
doesn't need the nuance.

**Restart behaviour:** pending approvals are in-memory only. A daemon
restart returns `504 Gateway Timeout` to every parked request. No
persistence in v1.

### SSE event stream

One SSE endpoint: `GET /dashboard/api/events`. Three event types in v1:

- `request` — an audit row was written. `id:` on the SSE frame is the
  `requests.id` ULID.
- `approval` — new pending approval created, includes the approval card
  fields.
- `approval-resolved` — a pending approval was approved / denied /
  timed-out.

**Backpressure (drop-on-full):** each subscriber has a 32-event buffered
channel. Broadcasts use a non-blocking send; if the buffer is full, the
event is dropped for that subscriber and the hot path continues. Matches
mcp-broker's pattern. Slow clients silently miss events; the initial page
load for the dashboard uses a paginated `/dashboard/api/audit` query, so the "what
happened while I wasn't watching" use case is served by the audit API, not
by the live feed.

**Keep-alive:** the server sends `:keepalive\n\n` every 15s so dead
connections are detected and subscriber channels get cleaned up.

### Admin auth

Single admin bearer token at `~/.config/agent-gateway/admin-token` (`0600`),
generated on first run, printed once to stdout. Presented via
`?token=<x>` on first load, then set as `HttpOnly; SameSite=Strict` cookie.
Rotatable via `agent-gateway token rotate admin`.

Protects every dashboard endpoint including writes (`/dashboard/api/decide`
for approval) and the SSE stream (`/dashboard/api/events`). Admin tokens and
agent tokens
are cryptographically distinct and live in different files/tables; no
cross-use. `GET /ca.pem` is the only explicitly unauthenticated
dashboard-port endpoint (public-key material by design).

**Unauthorized page has a re-auth form.** If the cookie is missing/expired
(private browsing, cache clear), the user lands on
`/dashboard/unauthorized` with a "paste admin token" input that posts to
the same token-promotion flow.

### Audit write path

One prepared INSERT per completed request (success or failure). Audit
errors are logged but never block the request pipeline
(`_ = auditor.Record(...)` pattern from mcp-broker). Sustained audit-write
failures are logged to stderr at `error` level.

Tunnel rows (non-MITM) have `interception='tunnel'`, `matched_rule=NULL`,
`method=NULL`, `path=NULL`, and `outcome='forwarded'`; only `host`, bytes,
and duration are known. No invisible traffic.

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
  master-key-<id>             # 0600, ONLY if OS keychain unavailable;
                              # <id> matches meta.active_key_id (seeded 1)

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
}

approval {
  timeout     = "5m"
  max_pending = 50      # global cap; 403 + Retry-After: 30 when hit
}

proxy_behavior {
  no_intercept_hosts = []
  max_body_buffer    = "1MiB"
}

timeouts {
  # agent-facing (we're the server)
  connect_read_header      = "10s"
  mitm_handshake           = "10s"
  idle_keepalive           = "120s"

  # upstream-facing (we're the client)
  upstream_dial            = "10s"
  upstream_tls             = "10s"
  upstream_response_header = "30s"
  upstream_idle_keepalive  = "90s"

  # body-buffering for matchers (bounded to defeat slowloris)
  body_buffer_read         = "30s"

  # deliberately unbounded (streaming correctness)
  request_body_read        = "0"
  response_body_read       = "0"
}

log {
  level  = "info"                # debug | info | warn | error
  format = "text"                # text | json
}
```

**Timeout rationale.** These values let Go's stdlib enforce slowloris
defences on the agent side while keeping streaming responses (SSE, Anthropic
streaming tokens) un-deadlined. `upstream_response_header = 30s` protects
against upstream hangs before the first byte; once bytes flow, the response
stream has no deadline. `body_buffer_read = 30s` caps how long body-matcher
buffering can stall a request — exceeding it bypasses body-matched rules
(same path as `> max_body_buffer`), which fails closed with 403 (see §4)
so an agent cannot evade a body-matched deny by stalling the upload.

**Phase ordering (for reference):**
`connect_read_header` → `mitm_handshake` → rule eval (instant) → approval
wait (if applicable; bounded by `approval.timeout`) → `upstream_dial` →
`upstream_tls` → `upstream_response_header` → streaming (unbounded).

`config refresh` re-reads defaults and preserves overrides (mcp-broker
pattern). `config edit` opens in `$EDITOR`. `config path` prints location.

### Startup sequence (`serve`)

1. Load + validate `config.hcl`. Fail fast on parse errors.
2. Open `state.db` with `busy_timeout=5s`, run idempotent migrations
   (`PRAGMA user_version` + numbered Go migration functions).
3. Load root CA (generate on first run).
4. Read `meta.active_key_id`. Resolve master key for that id from OS
   keychain, else file fallback, else (one-time) migrate from the
   pre-versioning `master-key` / `master.key` location, else generate +
   persist a fresh key.
5. Parse `rules.d/*.hcl`, validate HCL syntax and template syntax (not
   variable existence; see §4 two-phase validation). Fail startup on invalid
   rules.
6. Write PID file (see §3 CLI / daemon coordination).
7. Install `SIGHUP` handler for coarse reload.
8. Start background workers: audit prune, secret cache sweep, leaf-cert
   cache sweep.
9. Bind proxy (`:8220`) and dashboard (`:8221`).
10. If `open_browser` and stdout is a TTY and no `--headless`, open
    `http://127.0.0.1:8221/dashboard?token=<admin>` once.
11. Log startup summary: admin URL (first run only), agent count, secret
    count, loaded-rule count, MITM-eligible host list.

### Shutdown

`SIGTERM`/`SIGINT` → stop accepting new connections, 30s grace for
in-flight, cancel all parked approvals (agents receive
`504 Gateway Timeout`), close DB cleanly.

`SIGHUP` is the **primary CLI→daemon reload trigger** (see §3 CLI / daemon
coordination). Re-reads `config.hcl`, re-parses `rules.d/`, rebuilds the
agent prefix→hash map, invalidates the decrypted-secret LRU. Idempotent;
safe to send repeatedly.

### CLI surface

```
agent-gateway serve
agent-gateway config {path, edit, refresh}
agent-gateway rules {check, reload}
agent-gateway agent {add, list, rm, rotate, show}
agent-gateway secret {set, list, rotate, rm, master rotate}
agent-gateway ca {export, rotate}
agent-gateway token rotate admin
```

Cobra-based. `serve` holds a PID file in the config dir and refuses to
start if one is live (second instance would contend on DB + ports).

## 10. Threat Model

agent-gateway is a **confused-deputy system**: it holds real credentials on
behalf of sandboxed, untrusted agents and decides, per request, whether to
inject them. Every defense below exists to prevent an agent — or an operator
misconfiguration — from causing a real credential to reach a request that
shouldn't carry one.

### Trust boundaries

- **Untrusted.** The sandboxed agent — arbitrary code, arbitrary requests.
  The gateway must behave correctly for any traffic it sends, including
  deliberately malicious traffic.
- **Trusted.** The gateway process, its config files (`0o600`), the state
  DB, the master key (keychain or `0o600` file), and the dashboard operator.
- **Out of scope.** Host OS compromise, admin-token theft via filesystem
  read, and upstream server vulnerabilities. These are pre-conditions the
  gateway cannot defend against; callers must keep them intact.

### Attack classes and defenses

Each row names a concrete way injection could go wrong and the mechanism
that prevents it. File:line anchors are the authoritative places to review
the defense.

| # | Attack class | Concrete worry | Defense | Location |
|---|---|---|---|---|
| 1 | Host-suffix tricks | `github.com.attacker.com` masquerades as `github.com` | Host globs compile to anchored regexes (`^…$`); no substring matching | `internal/rules/parse.go:545` (`compileGlob`); regressions in `internal/rules/match_test.go` |
| 2 | Case / IDN / trailing-dot drift | `GitHub.Com.`, punycoded labels, or `github.com.` evade a literal rule `github.com` | Every host ingress point routes through `hostnorm.Normalize` / `NormalizeGlob` before comparison | Callers at `internal/proxy/connect.go:26`, `internal/proxy/decide.go:144`, `internal/rules/parse.go:278`, `internal/ca/leaf.go:83` |
| 3 | In-tunnel `Host:` header mismatch | Agent `CONNECT`s to `api.github.com:443` then sends `Host: attacker.com` inside the TLS tunnel | CONNECT target wins — `upReq.Host` is rewritten before rule evaluation | `internal/proxy/pipeline.go:336` |
| 4 | Redirect leakage | Upstream returns `302` to an attacker location, which would carry the injected credential forward | Proxy does a single `RoundTrip`; redirects are returned to the agent verbatim. A followed redirect triggers a fresh CONNECT, which re-enters decide → match → inject against the new host | `internal/proxy/pipeline.go:404` |
| 5 | Over-broad rules | Operator writes `host = "**"` or `no_intercept_hosts = ["*.com"]`, silently disabling MITM or scoping | Hard rejection of unambiguous cases (wildcard-only `no_intercept_hosts`, missing `match.host`). Soft warnings for legal-but-suspicious cases (`host = "**"`, public-suffix `no_intercept_hosts`) surface at load and in `rules check` | `internal/config/validate.go` `validateNoInterceptHosts`; `internal/rules/parse.go` `decodeMatchBlock` |
| 6 | Out-of-scope secret injection | A `*.example.com` rule matches and tries to inject a secret that was only intended for `api.github.com` | Per-secret `allowed_hosts` enforced hard at inject time. Scope mismatch → `ErrSecretHostScopeViolation` → `403 Forbidden`, audit `error='secret_host_scope_violation'`; request **never forwarded** | `internal/inject` (`Expand`); handler at `internal/proxy/pipeline.go:364-372` |
| 7 | Body matcher bypassed | Request body exceeds `max_body_buffer` or buffering times out; a `deny` rule with a body matcher can no longer fire | Fail closed — request blocked with `403` regardless of the rule's intended verdict; audit `error='body_matcher_bypassed:size\|:timeout'` | `internal/proxy/pipeline.go:245-257` |
| 8 | Missing secret at inject time | Rule references `${secrets.foo}` but `foo` is not in the store (typo, not-yet-created, deleted) | Fail soft — dummy credentials pass through untouched; audit `error='secret_unresolved'`. A real credential is never fabricated to fill the gap | `internal/proxy/pipeline.go` injection dispatch |
| 9 | Rule-reload failure | Operator `SIGHUP`s with a broken HCL file | Previous ruleset stays live; `atomic.Pointer` swap only on successful parse + compile | `internal/rules/engine.go:32-39` (`Reload`) |
| 10 | Agent impersonation | One agent tries to piggyback on another agent's rule scope or agent-scoped secret | Every CONNECT verifies `Proxy-Authorization` constant-time against the argon2id-hashed token store. The authenticated agent name — not the rule name — feeds agent-scoped secret lookup and the per-agent `HostsForAgent` index | `internal/agents/registry.go`; audit trail carries the authenticated name on every row |

### Non-goals

Explicit things agent-gateway does **not** try to defend against, so a
reviewer knows not to look for them:

- Compromise of the host OS or keychain. The master key is a local secret;
  an attacker with user-level access has it.
- Admin-token theft via filesystem read. `0o600` is the only protection.
- Upstream server compromise. A secret injected into a legitimate
  destination can still be misused by a compromised upstream.
- Traffic analysis. Destination hosts and timing are visible to anyone on
  the network path between gateway and upstream.
- Denial-of-service. No rate limiting on agent-facing ports; a malicious
  sandbox can exhaust gateway resources.
- Post-exfiltration detection. Once a secret has been legitimately
  injected, the gateway has no view into how the upstream handles it.

## 11. Prior Art & Attribution

agent-gateway draws on two reference points with fundamentally different
relationships.

### Relationship summary

- **mcp-broker** — sibling tool in this same monorepo, same author, same
  license. Meaningful reuse across auth, audit, dashboard embed, and CLI
  scaffolding. This is internal code reuse, not third-party incorporation.
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

### From mcp-broker (code carried over)

Transport-agnostic infrastructure ports near-verbatim. The pattern, not the
exact implementation:

- XDG-aware JSON/HCL config loader pattern.
- SQLite WAL audit module (extended schema — the verdict decomposition and
  streaming concerns are new).
- Bearer-token auth middleware with constant-time comparison and
  cookie-promotion.
- Embedded-HTML SPA with SSE event bus (same drop-on-full pattern;
  Last-Event-ID replay deferred to v1.1 — see §8).
- Cobra-based CLI with `config {path,edit,refresh}` UX.
- testify mocks + e2e subprocess tests with `testStack` wiring pattern.

### Net-new infrastructure (no prior art in-repo)

Code with no pattern to port from mcp-broker; written from scratch.

- PID file handling + daemon-reload signalling (`SIGHUP` handler, comm-name
  verification before signalling).
- Encrypted secret store (SQLite rows + AES-256-GCM) and master-key
  management (`go-keyring` with `master-key-<id>` file fallback,
  versioned via `meta.active_key_id`).
- Root-CA generation, persistence, and per-hostname leaf issuance with the
  `*tls.Config` cache and background sweeper.
- HCL rules parser and matcher (host/path globs, header regex, `json_body`
  / `form_body` matchers, RE2 compilation at load time).
- HTTP CONNECT handler and MITM pipeline (h1/h2 bridging, ALPN on both
  sides, upstream `http.Transport` with strict verification, streaming body
  forwarding).

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
- Match-and-swap model with tunnel-default for unmatched hosts (onecli
  post-v1.16 force-MITMs all authenticated traffic; we deliberately keep
  tunnel-default so unauthenticated transit like package-registry fetches
  works without per-host rules — see §6 CONNECT-time intercept decision).
- Rules-as-code HCL directory with CLI-triggered reload (onecli is
  dashboard-edit only).
- OS-keychain-protected master key (onecli uses an env var).
- HTTP/2 ALPN end-to-end.
- Agent-scoped secrets (stable-id audit trail across rotations is deferred
  to v1.1; v1 uses name + scope + `secrets.rotated_at`).

## 12. Open Questions & Risks

- **Timeout defaults are guesses.** `timeouts.*` values in §9 are reasonable
  starting points but unvalidated against real workloads. Slow-network
  legitimate uploads, long-lived Anthropic streaming sessions, and
  gRPC-over-h2 edges need real-world observation. v1.1 should revisit every
  number after ~1 month of production use, with metrics backing the choice.
- **Body buffering cap correctness.** `max_body_buffer = 1MiB` covers almost
  every API call but will block large uploads against any rule with a body
  matcher (fail-closed; see §4). The audit row's `error` column surfaces
  `body_matcher_bypassed:size` and `:timeout` tags; make sure the dashboard
  prominently shows these so users
  don't silently lose rule coverage on large payloads.
- **HTTP/2 end-to-end streaming.** Go's stdlib handles h1↔h2 bridging, but
  long-lived streaming responses (SSE from upstream, Anthropic streaming-
  token output) need verification. Milestone 2's e2e test should include a
  streaming-response fixture, not just request/response pairs.
- **Keychain availability on headless Linux.** `go-keyring` requires a
  running Secret Service daemon. Fallback to `master.key` file is fine but
  users on CI-ish machines will hit this. The startup warning must be loud
  and include remediation ("install gnome-keyring / keyring").
- **Rule ordering across files.** Lexical filename order is predictable but
  opaque at the UI level. The dashboard should render "effective order"
  (the flat evaluated list) as well as per-file views so users can audit
  priority at a glance.
- **Pinned-client detection.** We have no way to detect pinning before the
  handshake failure. Config-driven `no_intercept_hosts` is the only
  mitigation. A future improvement could observe repeated TLS verify
  failures from the agent side and auto-propose a pass-through entry.
- **Audit DB failure visibility.** A sustained audit-write failure (disk
  full, corruption) doesn't block requests, which is right for correctness
  but can go unnoticed in v1 since we only log to stderr. Revisit in v1.1
  with a dashboard `backlog-warning` event (deferred with the other
  nice-to-have SSE event types in §8).
- **Hostname normalization on rule and CONNECT-target ingest (shipped).**
  Canonicalisation lives in `internal/hostnorm`: `Normalize(host)` for bare
  hostnames (CONNECT target, cert cache key) and `NormalizeGlob(pattern)`
  for host-glob patterns (rule `match.host`, config `no_intercept_hosts`).
  Both lowercase via the IDNA `Lookup` profile, strip a single trailing
  `.`, and map Unicode labels to punycode. IP literals pass through
  unchanged. Normalization is applied at every ingress point — HCL parse,
  config load, CONNECT extraction, proxy pipeline handle, cert cache key,
  and `Decide()` — so the five paths cannot drift. Load-time
  normalisation is warn-on-change (operators see a one-liner telling them
  the stored form); runtime normalisation is silent with fallback to raw
  input on error. Mixed-wildcard segments (e.g. `api-*.github.com`) are
  ASCII-lowercased only — Unicode in mixed segments is unsupported and
  must be spelled ASCII. Tracked as audit finding #6.

## 13. Milestones

Rough sequencing for implementation planning. Each milestone lists its
acceptance criterion — the single test (or small set) that must pass to
consider the milestone done.

1. **Core skeleton.** Binary, config loader, XDG paths, SQLite migrations,
   CLI scaffolding. PID file + comm-name check on signalling.
   _Done when:_ `agent-gateway serve` binds both ports, `config {path, edit,
refresh}` works, `state.db` opens with WAL + `busy_timeout=5s`, second
   `serve` refuses due to PID file.
2. **CA + MITM plumbing.** Root CA generation, leaf issuance with
   per-hostname cache, CONNECT handler, ALPN on both sides.
   _Done when:_ `TestMITMEndToEnd` passes — a subprocess agent with
   `HTTPS_PROXY` set and our CA trusted makes a GET to an
   `httptest.Server`, the server sees a proxied request with a correct
   `Host` header and returns 200, and the agent sees the 200. Test covers
   both h1↔h1 and h1↔h2 bridging and a streaming-response fixture
   (chunked body flushed every 100 ms).
3. **Rule engine.** HCL loader, matcher, explicit reload via
   `rules reload` / SIGHUP, two-phase validation.
   _Done when:_ `TestRuleReloadHotSwap` passes — daemon starts with a rule
   referencing `${secrets.x}` (secret doesn't exist); `rules reload`
   succeeds; request matches rule and is audited as `injection='failed',
error='secret_unresolved'`. A second test edits the rule file to have
   invalid HCL and verifies the previous ruleset stays live.
4. **Secrets.** SQLite + AES-256-GCM, keychain + file fallback, CLI verbs,
   template expansion.
   _Done when:_ `TestSecretSubstitution` passes — CLI runs
   `secret set gh_bot`, daemon picks up via SIGHUP (not restart), next
   matched request has `Authorization: Bearer <real>` on upstream while
   agent only ever sees dummy.
5. **Audit log + dashboard live feed.** SQLite audit table with new column
   set, SSE endpoint with drop-on-full broadcast + 15s keepalive, paginated
   `/dashboard/api/audit` for initial page load, approve/deny UI.
   _Done when:_ `TestDashboardLiveFeed` passes — dashboard subscribes to
   `/dashboard/api/events`, 20 requests happen live, the browser sees all 20
   on the feed. A second test verifies the `/dashboard/api/audit` endpoint returns the same
   20 rows with correct pagination. A third test
   (`TestApprovalViewInvariant`) fires a request with a distinctive body
   and unasserted headers through a `require-approval` rule and asserts
   neither appears on the SSE `approval` event or the `/dashboard/api/pending`
   response (see §8 approval view invariant).
6. **Agents.** Token mint/auth, per-agent rule scoping (CONNECT-time
   filter), per-agent secret scoping, audit-field propagation.
   _Done when:_ `TestAgentScopeFilter` passes — two agents with different
   rule sets; each sees only MITM behaviour for their own scoped hosts;
   the other's scoped hosts tunnel.
7. **Polish.** Shadow warnings, retention pruning, startup summary,
   approval `max_pending` cap + 403 rejection, unauthorized re-auth form,
   README + this design doc.
   _Done when:_ the `agent-tools` repo-level `make audit` passes for the
   new tool, README includes install + first-run + prior-art sections,
   and a fresh-machine smoke test (trust CA, add agent, add secret, add
   rule, agent makes a request) succeeds in under two minutes.

## 14. Code Organization & Testing Patterns

### Interface boundaries

Each `internal/<pkg>` exports **one primary interface** (the port) and one
or more concrete implementations (the adapters). Other packages depend on
the interface, not the struct. Example shapes:

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

- **Unit tests** (`*_test.go`, default build): per-package, mock
  dependencies via `testify/mock`. Table-driven for matchers, parsers,
  template expansion, glob/regex edge cases. Should run in under 5 seconds
  for the whole repo.
- **Integration tests** (`//go:build integration`): real SQLite on
  `t.TempDir()`, real filesystem, real CA generation, real HCL parsing.
  Exercise two-or-three packages together (e.g. rules + secrets + inject →
  verify a rule actually produces the right header). No network, no child
  processes.
- **E2E tests** (`//go:build e2e`): built binary spawned as subprocess,
  real proxy port, real dashboard port, a mock upstream `httptest.Server`
  (we are a strict TLS client to it, so its cert is added to a test trust
  bundle). A test HTTP client sets `HTTPS_PROXY` to the subprocess, trusts
  the test CA, and makes real requests. Covers both intercept and tunnel
  paths.

Test helpers live in `test/testutil/` — a `Stack` builder that wires
real-or-mock components with one call, and per-layer defaults. Patterned on
mcp-broker's `testStack`.

### Conventions

- SQLite driver is `ncruces/go-sqlite3` (WASM-embedded, no CGO) — matches
  mcp-broker and keeps the monorepo CGO-free. All features this design
  relies on (WAL, partial indexes, JSON1 if needed) are supported. Requires
  the `embed` import alongside `driver`, per mcp-broker's convention.
- Constructors take context-free dependencies; `context.Context` is the
  _first_ parameter on every method that does I/O, cancellation, or audit.
- No package-level singletons. No `init()` side effects. Everything is
  constructed in `cmd/agent-gateway/serve.go` and passed down.
- Errors wrapped with `fmt.Errorf("doing X: %w", err)`; sentinels
  (`var ErrNotFound = errors.New(...)`) at package boundaries where callers
  need to branch.
- Structured logging via `log/slog`; a `slog.Logger` is a constructor
  dependency, not a global. Request ID threaded through `context.Context`
  (see §7 Request identity).
- No panic on the request path. Panics in background workers log +
  continue.
- Concurrency contracts documented on each interface: which methods are
  safe to call concurrently, which are not.

### Required patterns

Two patterns are load-bearing enough to call out explicitly:

**1. Context propagation for cancellation.**
The agent-facing `http.Request` has a context that Go's `http.Server`
cancels when the agent's TCP connection closes. Every upstream request in
`internal/proxy` **must** be built with
`http.NewRequestWithContext(agentReq.Context(), ...)` so that
agent-disconnect propagates to the upstream call and closes the upstream
TCP connection. Forgetting this creates zombie upstream requests when
agents Ctrl-C.

**2. Approval cleanup on agent disconnect (`ApprovalGuard`).**
A request parked in `internal/approval` subscribes to a decision channel
and blocks up to `approval.timeout`. If the agent disconnects mid-wait,
the pending entry should be removed immediately (not left to the 5-minute
timeout). Implementation pattern: register the pending entry, create a
cleanup closure, schedule it via `defer cleanup()`, and disarm the cleanup
only on a normal-path resolution:

```go
id := broker.register(pending)
disarm := false
defer func() {
    if !disarm {
        broker.remove(id)   // agent disconnected or context cancelled
    }
}()

select {
case decision := <-pending.ch:
    disarm = true
    return decision, nil
case <-ctx.Done():
    return nil, ctx.Err()   // guard's defer runs, cleans up pending
case <-time.After(timeout):
    disarm = true            // timeout path resolves normally
    return nil, ErrTimeout
}
```

This eliminates the "operator sees a ghost approval card for a request
whose agent is already gone" class of bugs, borrowed directly from onecli's
`ApprovalGuard` RAII pattern.
