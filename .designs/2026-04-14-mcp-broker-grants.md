# MCP Broker — Grant System

Date: 2026-04-14
Status: Design

## Summary

Add a **grant system** to the MCP broker that complements (does not replace)
the existing static rules engine. A grant is a short-lived, narrowly-scoped
authorization that lets an agent call specific tools with constrained
arguments. Grants are issued out-of-band by a human operator via
`broker-cli`, stored in SQLite, and presented by the agent on each request
via an `X-Grant-Token` HTTP header.

Grants are **purely additive**: they can only allow calls that would
otherwise require approval or be denied. They never restrict access. If a
presented grant doesn't match, the request falls through to the existing
rules engine unchanged.

## Goals

- Let an operator temporarily grant an agent the ability to call a specific
  tool with specific argument values (e.g. `git.git_push` with
  `branch=feat/foo, force=false`) for a bounded TTL.
- Make the grant's scope expressible without inventing a new matcher DSL.
- Audit every grant-related event so a postmortem can reconstruct what
  privilege the agent had and what it did with it.
- Zero behavior change for existing deployments that don't use grants.

## Non-Goals

- Multi-broker federation. Grants live in one broker's SQLite.
- Rate-limiting inside a grant window.
- Self-service grant creation from the dashboard (CLI-only for now, so the
  raw token never flows through a browser).
- Grant chaining (one session, multiple grants). Easy future extension;
  skipped for v1.

## Design Decisions

| Decision              | Choice                                                      |
| --------------------- | ----------------------------------------------------------- |
| Token transport       | `X-Grant-Token` HTTP header, connection-scoped              |
| Arg constraint format | JSON Schema subset (`santhosh-tekuri/jsonschema/v5`)        |
| Precedence            | Additive only — mismatches fall through to rules engine     |
| Usage bounds          | TTL only; unlimited uses within the window                  |
| Audit                 | Add `grant_id` + `grant_outcome` columns to `audit_records` |
| CLI create UX         | Per-constraint flags grouped by `--tool`; JSON file escape  |
| Dashboard             | Read-only "Grants" tab; create/revoke are CLI-only          |

## Data Model

### New table: `grants`

```sql
CREATE TABLE grants (
  id          TEXT PRIMARY KEY,        -- e.g. "grt_a1b2c3d4"
  token_hash  TEXT NOT NULL UNIQUE,    -- SHA-256 of bearer token
  description TEXT,
  entries     TEXT NOT NULL,           -- JSON array of {tool, argSchema}
  created_at  INTEGER NOT NULL,        -- unix epoch ms
  expires_at  INTEGER NOT NULL,
  revoked_at  INTEGER                  -- nullable
);
CREATE INDEX idx_grants_token_hash ON grants(token_hash);
CREATE INDEX idx_grants_expires_at ON grants(expires_at);
```

- **`id`** — short, human-friendly handle (`grt_` prefix + 8 random chars).
  Safe to log and display.
- **`token_hash`** — SHA-256 of the raw bearer token. The raw token is
  shown exactly once at creation and never persisted. Same pattern as
  GitHub and AWS API keys.
- **`entries`** — JSON array. Each entry binds one tool to a JSON Schema
  that its arguments must satisfy:

  ```json
  [
    {
      "tool": "git.git_push",
      "argSchema": {
        "type": "object",
        "properties": {
          "branch": { "const": "feat/foo" },
          "force": { "const": false }
        },
        "required": ["branch", "force"]
      }
    },
    { "tool": "git.git_fetch", "argSchema": { "type": "object" } }
  ]
  ```

- **Expired / revoked grants are retained** for audit forensics; they are
  filtered at match time (`expires_at > now() AND revoked_at IS NULL`).

### `audit_records` changes

```sql
ALTER TABLE audit_records ADD COLUMN grant_id      TEXT;
ALTER TABLE audit_records ADD COLUMN grant_outcome TEXT;
-- grant_outcome ∈ {matched, fell_through, invalid, NULL}
CREATE INDEX idx_audit_grant_id ON audit_records(grant_id);
```

- `grant_id` records the ID of the presented grant (if any), even when it
  didn't match, so operators can see "the agent tried to use grant X and
  fell through."
- `grant_outcome` distinguishes:
  - `matched` — grant authorized the call; rules engine was skipped
  - `fell_through` — valid grant presented but tool/args didn't match
  - `invalid` — token didn't exist, was expired, or was revoked
  - `NULL` — no grant token was presented
- The existing `verdict` column (`allow` / `deny` / `require-approval`)
  is unchanged. When `grant_outcome = matched`, `verdict = allow`.

Migrations follow the existing `audit.go` appended-on-load pattern and
must be idempotent (check via `PRAGMA table_info` before altering).

## Request Flow

Evaluation order in `broker.Handle()`:

```
1. HTTP bearer auth            (unchanged)
2. Extract X-Grant-Token       (new; absent is fine)
3. Grant evaluation            (new)
4. Rules engine                (unchanged, unless grant matched)
5. Approval gate / proxy / audit
```

### Grant evaluation algorithm

```go
// internal/grants/engine.go
func (e *Engine) Evaluate(token, tool string, args map[string]any) Result {
    if token == "" {
        return Result{Outcome: NotPresented}
    }
    grant, err := e.store.LookupByTokenHash(sha256(token))
    if err != nil || grant == nil || grant.Expired() || grant.Revoked() {
        return Result{Outcome: Invalid}
    }
    for _, entry := range grant.Entries {
        if entry.Tool != tool {
            continue
        }
        if entry.Schema.Validate(args) == nil {
            return Result{Outcome: Matched, GrantID: grant.ID}
        }
    }
    return Result{Outcome: FellThrough, GrantID: grant.ID}
}
```

### Wiring in `broker.Handle()`

```go
gr := grants.Evaluate(r.Header.Get("X-Grant-Token"), tool, args)
switch gr.Outcome {
case grants.Matched:
    record.GrantID, record.GrantOutcome = gr.GrantID, "matched"
    return proxy(tool, args)          // skip rules, skip approval
case grants.FellThrough:
    record.GrantID, record.GrantOutcome = gr.GrantID, "fell_through"
case grants.Invalid:
    record.GrantOutcome = "invalid"   // GrantID unknown
case grants.NotPresented:
    // no-op
}
verdict := rules.Evaluate(tool)        // existing path continues
```

### Properties

- **Matched** grants bypass the approval gate entirely. That is the whole
  point of a grant.
- **Invalid** tokens never cause a denial on their own. The request
  continues to the rules engine as if no token had been presented. Grants
  only add privilege; they do not subtract it.
- Compiled schemas are cached per grant ID and invalidated on revoke. The
  hot path is a `Validate(args)` call.
- The grants table is read-mostly. A single `sync.RWMutex` plus SQLite WAL
  mode is sufficient. No per-grant locks needed (no counters).

## CLI Surface

Three subcommands under `broker-cli grant`, talking to the broker over its
existing authenticated HTTP surface.

### Create

Grants are authored with **explicit per-constraint flags, grouped by tool**.
There is no YAML or file-based grant definition; the JSON Schema is built
by the CLI from the flags you pass. For schemas too rich to express with
the helper flags, a single `--arg-schema-file` flag takes over the whole
entry.

Tool names passed to `--tool` use the broker's existing
`<provider>.<tool>` namespaced form (same strings the rules engine
globs against, e.g. `git.git_push`, `github.gh_create_pr`). Bare
unqualified names are rejected.

**Common case** — single tool, equality on a couple of args:

```bash
broker-cli grant create --ttl 1h \
  --description "push feat/foo to origin" \
  --tool git.git_push \
    --arg-equal branch=feat/foo \
    --arg-equal force=false
```

**Multi-tool grant** — repeat `--tool`; each `--arg-*` attaches to the
most recent `--tool`:

```bash
broker-cli grant create --ttl 1h \
  --tool git.git_push \
    --arg-equal branch=feat/foo \
    --arg-equal force=false \
  --tool git.git_fetch
```

**Richer constraints**:

```bash
# regex
--arg-match branch=^feat/.*

# enum
--arg-enum remote=origin,upstream

# nested arg via dot-path
--arg-equal config.max_retries=3
```

**Escape hatch** — full custom schema from a JSON file for a single
entry:

```bash
broker-cli grant create --ttl 1h \
  --tool foo.complicated_tool --arg-schema-file complicated.schema.json \
  --tool git.git_push --arg-equal branch=feat/foo
```

#### Flag semantics

- `--tool NAME` opens a new entry. Subsequent `--arg-*` flags attach to
  it, until the next `--tool` or end of command.
- `--arg-equal key=value` → `properties.<key>.const = <value>` and marks
  `<key>` required. Value parsed as a JSON literal (`false`, `42`,
  `null`, `"x"`) with a bare-string fallback.
- `--arg-match key=PATTERN` → `properties.<key>.pattern = PATTERN`,
  required. Raw regex; no delimiters.
- `--arg-enum key=a,b,c` → `properties.<key>.enum = [a, b, c]`,
  required. Each element parsed as a JSON literal with bare-string
  fallback.
- `--arg-schema-file path.json` → the entry's `argSchema` is the file's
  contents verbatim. **Mutually exclusive** with any other `--arg-*`
  flag on the same entry; the CLI errors out if both are supplied. You
  either use the helpers _or_ give a complete schema — not both.
- Dot-path keys (`config.max_retries`) build nested
  `properties.<a>.properties.<b>…` structure; each level gets
  `type: object` and marks the child required. Real arg names
  containing `.` are rejected rather than introducing an escape syntax.

#### Validation before submit

Before calling `POST /api/grants`, the CLI fetches each referenced
tool's `InputSchema` via the existing `/mcp` surface and validates the
grant locally:

- Unknown arg names error with a levenshtein-based suggestion
  (`unknown arg "branc"; did you mean "branch"?`).
- Values are type-checked against the tool's schema
  (`--arg-equal force=feat/foo` errors because `force` is declared
  `boolean`).
- `--arg-schema-file` content is compiled with the same
  `santhosh-tekuri/jsonschema/v5` used at match time, so malformed
  schemas fail at the CLI rather than on the server.

This catches the typo-prone case at submission time instead of at the
next tool call.

#### Parsing note

`cobra/pflag` does not natively express "flags grouped by a preceding
delimiter flag." Implementation pre-processes `os.Args` to split on
`--tool` boundaries, then parses each group with a scoped flag set
before assembling the full grant. ~30 lines of code; same pattern
kubectl uses for multi-container pod specs.

**Output on success** (token shown exactly once):

```
Grant created.
  ID:          grt_a1b2c3d4
  Token:       gr_Xk8pQ2mN...zR9   ← copy now; will not be shown again
  Tools:       git.git_push
  Expires:     2026-04-14T18:42:00Z (in 1h)
  Description: push feat/foo to origin

Export it for an agent session:
  export MCP_BROKER_GRANT_TOKEN=gr_Xk8pQ2mN...zR9
```

### List

```bash
broker-cli grant list              # active grants only (default)
broker-cli grant list --all        # include expired / revoked
broker-cli grant list --json       # machine-readable
```

Columns: `ID | Tools | Expires | Status | Description`.

### Revoke

```bash
broker-cli grant revoke grt_a1b2c3d4
```

Sets `revoked_at = now()`. Idempotent (revoking an already-revoked grant
is a no-op, not an error).

### HTTP endpoints (new)

Exposed by the broker, consumed by the CLI:

```
POST   /api/grants              create; returns {id, token, …}
GET    /api/grants              list (query: ?status=active|all)
DELETE /api/grants/:id          revoke
```

All use the existing bearer auth. The `POST` response is the **only**
place the raw token appears.

## Dashboard

New **Grants** tab alongside Pending / Rules / Tools / Audit in
`internal/dashboard/index.html`.

- **Read-only.** No Create button, no Revoke button. All write operations
  are CLI-only so the raw token never touches a browser.
- **Columns**: `ID | Description | Tools | Expires | Status`
- **Status** chips: `active` (green), `expired` (grey), `revoked` (red).
  Default filter: `active` only.
- **Expand-row view** shows each entry's `argSchema` as pretty-printed
  JSON (reusing the tool-schema renderer from the Tools tab).
- **Audit tab updates**:
  - New filter: "has grant" (rows where `grant_id IS NOT NULL`)
  - New filter: grant-id search box
  - Rows with a grant show a pill — e.g. `🎫 grt_a1b2c3d4 (matched)` —
    clickable to jump to the grant row in the Grants tab.
- **SSE** — the `/events` stream gets `grant.created` / `grant.revoked`
  events. Expiry is computed client-side from `expires_at` — no
  server-side sweeper needed.

## Security

- Bearer tokens are 32 random bytes, base64url-encoded, presented as
  `X-Grant-Token: <raw-token>`.
- Only `SHA-256(token)` is persisted; the raw token is shown once at
  creation. Compromise forensics still work via the always-visible
  `grant_id`.
- Invalid grant tokens are logged (`grant_outcome = invalid`) but never
  cause a request to fail — grants are additive.
- Revocation is immediate: the next `Evaluate` sees `revoked_at != NULL`
  and returns `Invalid`. The compiled-schema cache is invalidated on
  revoke.
- Clock skew is not a concern: the broker and its agents are colocated on
  the same host.

## Testing

- **Unit (`grants.Engine.Evaluate`)** — table-driven across: matched,
  fell-through, invalid, not-presented, expired, revoked. Plus schema
  validation edges: missing required field, extra disallowed field,
  nested-object args.
- **Unit (CLI schema builder)** — each `--arg-*` flag builds the right
  JSON Schema fragment: `--arg-equal` → `const` + required;
  `--arg-match` → `pattern` + required; `--arg-enum` → `enum` + required;
  dot-path keys nest `properties` correctly. `--arg-schema-file` errors
  when combined with any other `--arg-*` flag for the same entry.
- **Unit (pre-submit validation)** — unknown arg name triggers a
  suggestion; mistyped value type (`force=feat/foo` where `force` is
  `boolean`) is rejected before the HTTP call.
- **Integration** — boot broker with in-memory SQLite; hit `/mcp` with
  various `X-Grant-Token` states; assert `audit_records` carry the right
  `grant_id` / `grant_outcome`; assert invalid token does **not** deny;
  assert fall-through correctly hits the rules engine.
- **Migration** — run against a DB populated with pre-grant audit rows;
  confirm `ALTER TABLE` is idempotent.

## Package Layout

```
mcp-broker/internal/grants/
  engine.go       # Evaluate(token, tool, args) -> Result
  store.go        # SQLite CRUD; token hashing
  schema.go       # JSON Schema compile + validate
  api.go          # HTTP handlers for /api/grants
  types.go        # Grant, Entry, Result, Outcome
```

Plus:

- `broker-cli/cmd/broker-cli/grant.go` — new Cobra subcommand tree.
- New dependency: `github.com/santhosh-tekuri/jsonschema/v5`.
- `mcp-broker/internal/dashboard/` — template + API updates for the
  Grants tab.

## Rollout

- No feature flag. Grants are purely additive — a broker without any
  grants in its DB behaves exactly as today.
- Logical commits: (1) schema + engine, (2) HTTP API, (3) CLI,
  (4) dashboard tab. Any of these can ship independently; the system
  works from commit (1) onward via direct DB insertion.

## Open Questions / Future Work

1. **Multi-broker deployments** — grants are single-broker. Document;
   revisit if/when we deploy the broker beyond one host.
2. **Grant chaining** — single header means one grant per agent session.
   Future: accept a comma-separated list and try each in order. No
   schema change needed.
3. **Rate limits** — a grant with a wide-open TTL has no per-second
   ceiling. Mention in docs; revisit if a real use case emerges.
4. **Dry-run matching** — a `broker-cli grant test <id> --tool X --args
'{...}'` command could tell you whether a grant _would_ match a call
   without actually making the call. Nice-to-have; defer.
