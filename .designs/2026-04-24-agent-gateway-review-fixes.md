# agent-gateway — review fixes

Date: 2026-04-24
Scope: agent-gateway

## 1. Context, scope, and non-goals

### Context

A deep review of `agent-gateway` on 2026-04-24 surfaced 4 P0 security/correctness gaps, 10 P1 ergonomic/correctness issues, and 12 P2 polish items. A walkthrough with the maintainer produced decisions for every item, recorded in this design. The goal is to transition the tool from "safe by default if you know the conventions" to "hard to accidentally misuse."

### Scope

Code changes across `internal/proxy/`, `internal/config/`, `cmd/agent-gateway/`, plus new centralized validators and broader use of existing ones. Documentation changes across `docs/rules.md`, `docs/cli.md`, `README.md`, and a new `docs/config.md`. Two small CLI surface changes: rename `rules reload` → top-level `reload`; add `--strict` to `rules check` and `--output json|text` to two `list` commands.

### Non-goals

- **P1-9** Warning about in-flight sandboxes on `agent rotate`/`agent rm`. Operator is already prompted and knows the token dies.
- **P2-1** Renaming `config refresh`. Cross-tool naming consistency wins over in-tool verb disambiguation.
- **P2-4** Stable exit-code taxonomy. Not worth the public-API commitment; the one distinction with real value (daemon running vs not) is covered by P2-5 instead.
- **P2-8** `secret add --file`. `< file` redirection works and is the Unix convention.
- **Hot-reload for any `config.hcl` field.** The file is fully restart-only to keep the mental model consistent; `reload` detects changes via hash and warns.
- **Auto-restart command.** Warnings only; operator performs the restart.

### Success criteria

- All P0 items landed before the next release cut.
- No regression in existing integration and e2e tests.
- New tests for every P0 and each P1 behavioral change.
- `make audit` green.

## 2. Pipeline security batch (P0-1, P0-3, P1-3, P1-4)

All four changes are in `internal/proxy/pipeline.go`, on the request-handling hot path. Grouped together because they share a test fixture and touch the same response-writing code.

### P0-1 Unknown verdict → deny

`pipeline.go:335-339` `default` branch flips from allow to deny. Emit an audit row with `error='unknown_verdict'` and response reason `unknown-verdict`. The parser already rejects unknown verdicts at load time (`parse.go:205-209`), so the branch is only reachable via version skew or memory corruption — fail-closed is the correct default.

### P0-3 Redact sensitive headers in approval view

`assertedHeaders` (callsite at `pipeline.go:302-309`) currently forwards raw header values to the approval UI. Change: for any header whose lower-cased name is in the hard-coded set `{authorization, proxy-authorization, cookie, set-cookie, x-api-key, x-auth-token}`, replace the value with the literal string `<redacted>` before the `ApprovalRequest` struct is built. Header names remain — the operator sees _which_ header the rule matched without seeing the value. Not configurable.

### P1-3 Queue-full → 503 + Retry-After

`pipeline.go:310-315` currently returns 502 for any `apErr`. Branch on `errors.Is(apErr, approval.ErrQueueFull)` → 503 with `Retry-After: 30`. All other approval errors stay 502. Sandbox HTTP clients will back off instead of retrying into a saturated queue.

### P1-4 Better 403 body + `X-Agent-Gateway-Reason` header convention

Two parts:

1. The body-matcher 403 at `pipeline.go:278-280` names the cap: `Forbidden: body exceeds max_body_buffer (1 MiB); raise proxy_behavior.max_body_buffer in config.hcl`. Use `humanSize(p.maxBodyBuffer)`.
2. Adopt `X-Agent-Gateway-Reason` on every proxy 4xx/5xx. Documented code list: `body-matcher-bypassed`, `approval-denied`, `approval-timeout`, `queue-full`, `secret-unresolved`, `unknown-verdict`, `rule-deny`, `forbidden-host`. Values are stable strings; document in `docs/security-model.md` (reason-code section). Add one helper in `pipeline.go` to set the header alongside `http.Error`.

### Testing

Unit tests: unknown-verdict → 403 + deny audit row; sensitive headers redacted in approval event payload; `ErrQueueFull` → 503 with `Retry-After`; 403 body contains cap string and reason header; every 4xx/5xx path carries a reason header.

## 3. Startup/fatal and observability (P0-2, P2-9)

Both changes live in `cmd/agent-gateway/serve.go`, on the boot path before the listeners come up.

### P0-2 Secrets store unavailable → fatal

`serve.go:173-179` currently logs a warning and sets `proxyInjector = nil` if `secrets.NewStore` fails. Change: return the error from `execServe` so the daemon exits nonzero with a clear message printed to stderr:

```
agent-gateway: secrets store unavailable: <err>
  The daemon requires a working secrets store to inject credentials.
  If the keychain is unavailable, ensure the file fallback path is readable.
```

Drop the `secretsErr` / nil-injector branches — with this change, `inj` is always non-nil past this point, so the pipeline code can assume a valid injector and drop its own nil checks (small simplification in `pipeline.go` and `pipeline_test.go`).

Rationale: the previous behavior booted into a silent degraded mode where every credential-requiring rule passed the dummy token through, indistinguishable from "no rule matched" in the audit log. Failing loudly at boot matches the overall fail-closed posture.

### P2-9 Log startup paths

After the listeners are up but before the existing `"agent-gateway started"` line at `serve.go:341-345`, print resolved paths to stdout and slog. Exactly these fields:

```
config:    /home/avery/.config/agent-gateway/config.hcl
state_db:  /home/avery/.local/share/agent-gateway/state.db
ca_cert:   /home/avery/.local/share/agent-gateway/ca.crt
pid_file:  /home/avery/.config/agent-gateway/agent-gateway.pid
```

`ca_key` is deliberately omitted — the path of a private key is operational knowledge, not debug output. Operators who need it look it up themselves.

Format on stdout mirrors the existing `Dashboard: …` / `Proxy: …` lines so `systemd`/`launchd` stdout captures it. Slog version uses structured fields (`"config", path`) for machine parsing.

### Testing

- Serve test: inject a broken secrets store, assert `execServe` returns an error and listeners never bind.
- Serve test: assert startup output contains the four path labels with resolved XDG paths.

## 4. Validation centralization (P0-4, P1-2, P1-7, P1-8)

Four related changes that share `warnSecretCoverage` and config validators. Goal: every validation warning or error has one owner, and every caller (daemon start, SIGHUP, `rules check`, CLI mutations) routes through it.

### P0-4 `rules check` runs secret coverage

`warnSecretCoverage` stays in `cmd/agent-gateway/secret_coverage.go` — it's CLI-level glue (returns human strings, not suitable for `internal/`). Add a third caller in `execRulesCheck`:

1. Open state DB read-only via `store.OpenReadOnly` (add this helper if not present). If DB absent, print `note: state DB not found; skipping secret coverage check` and skip.
2. If DB present, load `secrets.Store` and call `warnSecretCoverage`. Write results to stdout.
3. Add `--strict` flag. If set and any warnings emitted, exit nonzero after printing.

### P1-2 `no_intercept_hosts` × rules overlap warnings

New function `warnNoInterceptOverlap(engine *rules.Engine, patterns []string) []string` in `cmd/agent-gateway/secret_coverage.go` (same file, same shape as `warnSecretCoverage`). For each entry in `patterns` that has any rule whose `match.host` could plausibly overlap (louder/conservative matching via `internal/hostmatch`), emit one warning listing every shadowed rule with file path:

```
warning: proxy_behavior.no_intercept_hosts[0] "api.github.com" shadows:
  - rule "github-user" (rules.d/10-github.hcl) match.host "api.github.com"
  - rule "github-repo" (rules.d/10-github.hcl) match.host "*.github.com"
```

Called from `serve.go` startup + SIGHUP and `execRulesCheck`, same pattern as `warnSecretCoverage`.

### P1-7 Public-suffix `no_intercept_hosts` → hard reject

`validate.go:170-194` currently warns on public-suffix matches. Flip to error. Matches existing `allowed_hosts` behavior. No escape-hatch flag. Existing warning test → error test.

### P1-8 CLI-side coverage warnings on mutations

Every mutating secret/rule CLI (`secret add`, `secret update`, `secret rm`, `secret bind`, `secret unbind`) calls `warnSecretCoverage` after the DB write and before the best-effort SIGHUP. Warnings go to stdout. Daemon still logs on SIGHUP — double-exposure is intentional (CLI output for the operator, slog for postmortem).

### Testing

Coverage helper table tests for the overlap detector. Serve and rules-check tests assert warnings appear in the expected streams. Existing `validate_test.go` updated from warn to error assertion.

## 5. Reload and config model (P1-1, P1-10, P2-5)

The reload story is currently split between `rules reload` (a SIGHUP that does far more than rules) and the undocumented need to manually restart for `config.hcl` edits. This section consolidates it.

### `rules reload` → top-level `reload`

Move the Cobra command from `cmd/agent-gateway/rules.go` to a new `cmd/agent-gateway/reload.go` as a top-level subcommand. The underlying SIGHUP behavior doesn't change — still reloads rules, agent registry, secrets cache, admin token, and CA. Keep `rules reload` as a hidden alias for one release that prints a one-line deprecation notice to stderr and delegates to the new command.

### `reload` requires a daemon

Current `rules reload` returns 0 when no daemon is running ("state is still valid on disk"). Change: standalone `reload` errors with `no daemon running; start it with 'agent-gateway serve'` and exits nonzero. Auto-reload from mutations (`secret add`, etc.) stays best-effort silent — the DB write succeeded, SIGHUP is opportunistic, daemon picks up changes on next start.

### `config.hcl` is fully restart-only

Document the rule in `docs/config.md` (section 8) and `CLAUDE.md`. Two warning paths alert the user:

**`config edit` diff.** After the editor exits, compute a diff of the parsed pre/post config. If any field changed, print:

```
warning: config.hcl has changed. These edits require a daemon restart:
  - approval.timeout:    5m → 30m
  - secrets.cache_ttl:   10m → 1h
Apply with: kill <pid> and re-run 'agent-gateway serve'.
```

**`reload` hash check.** Daemon writes `sha256(config.hcl)` to the SQLite `meta` table on startup (key `config_hash`). `reload` reads the stored hash and compares against `sha256` of the current file. Mismatch → same warning as above (without the field-level diff — hash doesn't tell you what changed). Still does the SIGHUP.

### Subsumed item (P1-10)

SIGHUP's inject-cache invalidation stays — it picks up `secret update` mutations from SQLite, which is valid regardless of `cache_ttl`. Documentation updated to frame it that way, not as "applies TTL changes."

### Testing

- Reload unit test: no daemon → error + nonzero exit; running daemon → SIGHUP sent.
- Config edit test: mock editor that mutates the file, assert warning emitted listing exact changed fields.
- Hash check test: pre-seed `meta.config_hash` to a stale value, assert reload warns.
- Deprecation alias: `rules reload` emits notice and delegates.

## 6. Audit, CLI polish, and documentation

Small code changes and a focused docs pass.

### P1-5 Populate audit `Query` field

`pipeline.go:164` currently writes only `r.URL.Path`. Change: set `entry.Query = truncate(r.URL.RawQuery, 2048)` when non-empty. `truncate` returns the first 2 KB with `…` appended if the source exceeds the cap. No redaction — the sandbox's dummy tokens are the only credential material that ever reaches query strings by design.

### P2-2 `Long` blocks on destructive commands

Add `Long` to `master-key rotate`, `admin-token rotate`, `ca rotate`, `agent rm`, `secret rm`. Content: one line matching `Short`, then "Immediate consequences:" bullet list, then "Recovery:" paragraph where applicable (currently only `master-key rotate`, per `CLAUDE.md:90`). `Short` unchanged — stays scannable in `agent-gateway --help`.

### P2-3 `--output json|text` on list commands

Only `agent list` and `secret list`. Add `-o/--output` flag; default `text`. JSON schemas documented in `docs/cli.md`:

```json
{"agents": [{"name": "...", "prefix": "...", "created_at": "...", "last_seen_at": "..."}]}
{"secrets": [{"name": "...", "allowed_hosts": ["..."], "bound_rules": ["..."], "created_at": "..."}]}
```

Token hashes and secret values never appear — same surface as `text` output.

### Documentation pass (P1-6, P2-6, P2-7, P2-10, P2-11, P2-12)

One coherent documentation change, landed as a single commit at the end:

- **`docs/rules.md` `match.host` section**: add side-by-side glob semantics table (`api.X` / `*.X` / `**.X`) per P1-6, and a CONNECT-host callout explaining match is the CONNECT target, not the inner `Host:` header (P2-11).
- **`internal/config/default.hcl:49-50`**: inline comment on `request_body_read` / `response_body_read` explaining `0s` = no deadline (P2-6).
- **`examples/rules.d/*.hcl`**: prepend a `# Setup: echo -n "..." | agent-gateway secret add <name> --host <host>` comment block to every example referencing `${secrets.X}`. Uses `00-github-denylist.hcl:1-63` as the template (P2-7).
- **New `docs/config.md`**: hand-written, one table per config block (field, type, default, description). All fields marked restart-required, referencing the rule from section 5. This absorbs the scattered config references (P2-10).
- **`README.md`**: new sections for "Stopping" (SIGTERM), "Upgrading" (schema migrations are automatic, downgrades unsupported), "Single-instance per user" (PID-file and bind-conflict behavior) (P2-12).

### Testing

- Audit unit test: request with long raw query → stored with truncation indicator.
- Help-text test: destructive commands have `Long` matching expected shape.
- List command tests: JSON output parses and contains expected fields, no sensitive values.
- `make audit` validates lint/fmt on the new files.

## 7. Rollout order

Recommended batching and order of landings:

1. **Pipeline security batch** (section 2) — P0-1, P0-3, P1-3, P1-4. Single PR; all in `pipeline.go`.
2. **Startup/fatal batch** (section 3) — P0-2, P2-9. Single PR; `serve.go` + small pipeline simplification.
3. **Validation centralization batch** (section 4) — P0-4, P1-2, P1-7, P1-8. Single PR; touches `secret_coverage.go`, `validate.go`, `rules.go`, and all secret CLI files.
4. **Reload/config batch** (section 5) — P1-1, P1-10, P2-5. Single PR; creates `reload.go`, adds hash check to `serve.go` and `reload.go`, updates `config edit`.
5. **Audit/CLI polish batch** (section 6, first half) — P1-5, P2-2, P2-3. Single PR.
6. **Documentation batch** (section 6, docs pass) — P1-6, P2-6, P2-7, P2-10, P2-11, P2-12. Single PR; docs only.

Steps 1–4 can overlap in review if reviewers have bandwidth; 5 and 6 can land in parallel once 1–4 are merged.

## 8. Open questions

None blocking. All design decisions confirmed during the 2026-04-24 walkthrough.
