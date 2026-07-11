# Pi Session Analyzer Plan

## Goal

Build `pi-session-analyzer`, a local, passive Go tool that ingests Pi coding-agent JSONL session logs into a private SQLite database, reports session summaries, detects Pi-specific failure modes, and exposes bounded read-only MCP queries for cross-session analysis.

The first release must provide useful retrospective diagnostics without becoming a live proxy, generic tracing UI, cost product, or autonomous configuration-editing loop.

## Background / Repo Context

- The `research` branch contains the source research and an earlier plan under the working name `pilens`. This plan supersedes that implementation shape on the current branch; implementation must not modify the research worktree or branch.
- The measured corpus contained 154 Pi sessions, 71 MB, and 22,633 JSONL records. Pi logs contain seven top-level record types: `session`, `message`, `model_change`, `thinking_level_change`, `compaction`, `custom`, and `custom_message`.
- Pi-specific records expose signals generic provider traces miss: goal/TODO snapshots, broker-guard messages, compaction state, tool results, stop reasons, and per-message usage/cost.
- Historical Pi transcripts do not reliably propagate MCP `isError`: observed `mcp_call` failures appear in result text. However, `mcp-broker/cmd/mcp-broker/serve.go` already emits structural MCP `CallToolResult.IsError`, and `mcp-broker/test/e2e/e2e_test.go` proves this for broker denials. The missing MCP-response-to-Pi-transcript propagation is outside this repository. The analyzer must therefore prefer structural data when present and retain a text fallback for historical logs.
- New Go tools are independent modules. Follow the repository checklist in `AGENTS.md`: module scaffold, root `Makefile`, `go.work`, root indexes, tool documentation, `AGENTS.md -> CLAUDE.md`, and `assets/tool-relationships.svg`.
- Mirror the CLI/MCP organization in `telegram-mcp/` and `local-git-mcp/`. Mirror pure-Go SQLite initialization, permissions, WAL handling, and migrations from `mcp-broker/internal/audit/`.
- Use the repository Go version from `go.work` (currently 1.25.11), current repository dependency versions where applicable, and the standard per-tool `Makefile`/`.golangci.yml` conventions.

## Acceptance Criteria

### Module and ingestion

- **AC-1:** `pi-session-analyzer/` is an independent cgo-free Go module, participates in root build/test/audit targets, and follows the repository's documented module, CLI, test, and documentation layout.
- **AC-2:** `pi-session-analyzer ingest` parses all seven known Pi JSONL record types using type dispatch, preserves source-line provenance, and tolerates unknown record types, unknown session schema versions, malformed middle records, and a torn final line without losing valid records from the same session.
- **AC-3:** Assistant thinking content and `thinkingSignature` are never retained; usage reasoning counts remain available. Streamed or repeated assistant message IDs and repeated ingestion cannot double-count messages, tokens, costs, events, or findings.
- **AC-4:** Every free-text or JSON-blob field passes through a credential-value scrubber before SQLite persistence. Tests cover supported credential patterns, near misses, idempotence, and byte-identical preservation of detector markers such as `FAIL`, `Error Trace:`, `mcp_call failed`, `MCP error`, and `fetch failed`.
- **AC-5:** The analyzer-owned XDG leaf directory is mode `0700`; the SQLite database and every recreated sidecar are repaired to mode `0600` after open and writes. No ancestor XDG directory is chmodded, and no schema or write path stores raw unsanitized content. Indexed sessions remain queryable when their source JSONL file is later deleted; ingest never implicitly prunes absent sources.

### CLI and findings

- **AC-6:** The CLI provides `ingest`, `list-sessions`, `session-summary`, `detect`, and `mcp` commands with command-specific argument errors and documented XDG-aware defaults for the Pi sessions directory and database path.
- **AC-7:** Session summaries expose cost; output, reasoning, and cache-read usage without presenting `totalTokens` as generated work; per-tool calls/errors; final stop reason; goal terminal state or `no goal started`; compactions; broker guards; schema drift; and findings.
- **AC-8:** Findings have a persisted and user-visible classification of `structural` or `heuristic`, a fixed `error|warn|info` severity, detector identity, traceable evidence IDs/line provenance, and machine-readable details. CLI and MCP outputs never present heuristic findings as structural facts.
- **AC-9:** Structural detectors cover broker-guard events, compaction pressure/provider failure, tool-error bursts, and MCP failures. MCP failure detection uses structural `is_error` when present and historical marker matching otherwise, and reports which evidence source fired.
- **AC-10:** Guarded heuristic detectors cover stuck retry loops, silent close after a started-but-incomplete goal, code changes without later verification, editing an existing path without a prior read, and terminal provider/user interruption states. Tests include healthy counterexamples for repeated changing test output, goalless sessions, docs/config-only edits, new files, shell-based reads, and benign user cancellation.
- **AC-11:** Ingest automatically recomputes findings only for newly ingested or changed sessions; `detect` can idempotently recompute all sessions or one unambiguously identified session.

### MCP and integration

- **AC-12:** `pi-session-analyzer mcp` serves stdio MCP tools `list_sessions`, `session_summary`, `top_failures`, `get_conversation`, `get_message`, and `run_select`. Every tool is annotated read-only, non-destructive, and closed-world, with a test covering the complete registry.
- **AC-13:** All MCP responses pass through one output-cap implementation, remain valid JSON when truncated, and enforce documented limits: at most 1,024 SQL rows, approximately 50,000 response characters, bounded list/drill-down counts, and a five-second SQL timeout.
- **AC-14:** `run_select` executes on a separate SQLite `mode=ro` connection with `query_only` enabled. It accepts one `SELECT` or CTE statement, rejects multiple statements and write/schema/attachment pragmas, and applies the row cap independently of caller SQL. Tests prove both accepted and rejected query classes; lexical checks are defense in depth rather than the write-safety boundary.
- **AC-15:** `mcp-broker` tests demonstrate that broker-originated denials, upstream tool results with `IsError=true`, and backend call failures reach MCP clients as structural `CallToolResult.IsError=true`. No broker behavior change is made unless a test exposes a real propagation defect.

### Verification and documentation

- **AC-16:** Synthetic fixtures and tests cover parser drift/torn lines, scrub invariants, idempotent storage, source-deletion retention, detector firing/non-firing guards, command behavior, MCP annotations/caps, and read-only SQL enforcement. Real private session records are never committed as fixtures or test output.
- **AC-17:** A manual real-corpus run ingests every currently available session without fatal errors. The durable `/goal` completion evidence and final implementation summary record the tested commit, exact commands, aggregate counts only, and confirmation that a second unchanged run ingests zero changed sessions.
- **AC-18:** Detector quality is reviewed against the real corpus before completion: review every structural finding when a detector produces at most 50 findings, otherwise the first 20 from a stable `(detector, session_id, first_evidence_id)` ordering; review up to the first 20 findings in that same ordering for each heuristic detector. The durable `/goal` completion evidence and final implementation summary record per-detector population/sample sizes, apparent false-positive counts, and any resulting threshold changes without quoting private transcript content. No separate tracked report containing corpus information is created.
- **AC-19:** Tool and root documentation explain installation, commands, MCP registration behind `mcp-broker`, data retention, credentials-only scrubbing, the private/non-share-safe trust boundary, detector classifications, SQL/output limits, and explicit non-goals. The root architecture diagram validates as XML, renders successfully, and is visually inspected.

## Non-Goals / Out of Scope

- Browser dashboards, transcript trees/timelines, or a shareable report/export format.
- Live tailing, file watching, or placing the analyzer on Pi's provider/MCP critical path.
- Multi-user service operation, authentication, authorization, tenant isolation, or remote database access.
- Identifier redaction. Repository paths, usernames, hostnames, and URLs remain available for local diagnostics, so the database and MCP responses are not safe to share by default.
- Cost analytics as a standalone product; cost is session context only.
- Statistical severity/quality ranking, precision/recall claims, or probabilistic failure scoring before a maintained labeled gold set exists.
- Correction-rate metrics based on parent branching; the measured Pi corpus is linear.
- Configuration cohorts, configuration-change proposals, automatic edits, or reflection loops. These remain blocked until Pi records a complete session-start configuration fingerprint, including currently machine-local settings.
- Capturing a Git SHA as a substitute for complete Pi configuration provenance.
- Automatic deletion when source logs disappear. A future explicit prune workflow is outside v1.
- Fixing Pi's MCP-response-to-transcript serialization, which is not implemented in this repository.
- OpenInference/Phoenix export and other observability integrations.

## Constraints

- Work only on the current branch/worktree. Treat the research worktree as read-only source material.
- Keep the tool cgo-free. Use `github.com/ncruces/go-sqlite3` following `mcp-broker/internal/audit` rather than introducing DuckDB/cgo.
- The on-disk Pi logs remain the raw source of record. The analyzer stores only scrubbed normalized data and provenance.
- The v1 trust boundary is one local user. Default storage is under an analyzer-owned XDG leaf directory mode `0700`, with the database and SQLite sidecars mode `0600`. Create or repair only that leaf and its files; never chmod shared XDG ancestors.
- Scrub credential values only. Cover tokens, API keys, passwords, authorization values, JWTs, and private-key blocks; avoid entropy-based redaction and preserve surrounding diagnostic text.
- Do not persist assistant thinking content, encrypted signatures, or any parallel raw-text tier.
- Use synthetic JSONL fixtures only. Never copy real private session lines into the repository, test snapshots, logs, or completion report.
- Prefer Go standard-library functionality. Add a projection dependency only if the standard library cannot reasonably support the documented `get_message` field projection contract.
- All MCP access is read-only. The analyzer exposes no mutation, proposal, or configuration tools.
- Discovery and analysis are not authorization. Running behind `mcp-broker` does not make analyzer output share-safe.

## Chosen Approach

Implement one independent Go module with a one-way pipeline:

```text
Pi JSONL files
  -> tolerant typed parser
  -> credential-value scrubber
  -> normalized private SQLite store
  -> deterministic detector engine
  -> CLI summaries and bounded read-only stdio MCP
```

The JSONL parser performs explicit top-level type dispatch because naive message-only projection drops Pi-specific state. Storage uses normalized tables and transactional session replacement so changed files can be re-ingested idempotently. The source file path, source line, message ID, and tool-call/result IDs provide finding provenance.

Detectors are deterministic code over the normalized store; no LLM participates in detection. Findings are divided into:

- **Structural:** directly supported by explicit records or error fields, with historical MCP marker matching identified as a compatibility fallback.
- **Heuristic:** behavior inferred from event sequences, always carrying false-positive guards and visibly labeled in every interface.

The MCP surface is summary-first and progressively reveals bounded detail. Dedicated tools cover common analysis; guarded `run_select` permits local exploratory analysis without making the database writable. Generic visualization and config-editing are deliberately deferred.

## Design Decisions

- **D1 — Name:** Use `pi-session-analyzer` for the module, binary, database directory, MCP server name, documentation, and diagram. This follows descriptive object-role names already used in the repository.
- **D2 — Trust boundary:** Support local single-user use only. Preserve useful identifiers, scrub credentials, and document that outputs are private and non-share-safe.
- **D3 — Scope:** Deliver ingest/summaries, detectors, and read-only MCP in v1. Reassess visualization, export, and reflection only after this core is used.
- **D4 — Storage:** Stay cgo-free with pure-Go SQLite. Scrub before persistence and keep no raw tier. Replace a changed session transactionally; skip unchanged `(size, mtime)` files.
- **D5 — Retention:** Ingest is additive/update-only. Missing source files do not delete indexed sessions; future pruning must be explicit.
- **D6 — Finding semantics:** Persist `structural|heuristic` separately from `error|warn|info`. Frequency and fixed severity may order output, but the tool must not claim statistically calibrated rankings.
- **D7 — MCP errors:** Prefer `tool_result.is_error`; retain narrow historical text markers for transcripts where Pi discarded MCP `isError`. `mcp-broker` already emits structural errors at its MCP boundary, so strengthen propagation coverage rather than adding redundant broker behavior. Pi transcript propagation remains an external dependency.
- **D8 — Configuration provenance:** Do not record an incomplete surrogate. Cohort/reflection work starts only after Pi emits a complete config fingerprint per session.
- **D9 — Agent query interface:** Include bounded dedicated tools and guarded `run_select`. Read-only SQLite mode is the enforcement boundary; SQL keyword checks, statement checks, and wrapping limits are defense in depth.
- **D10 — Detector quality:** Require synthetic guard tests plus a privacy-safe sampled review against the real corpus. Heuristics remain explicitly provisional without a labeled gold set.

## Data and Interface Shape

The implementer owns exact migrations and Go types, but the persisted model must support these concepts:

- `sessions`: Pi session identity, source path and file metadata, cwd, schema version/drift, timestamps, ingest statistics.
- `messages`: role, parent/message IDs, source line, timestamp, model, stop reason, scrubbed text, token categories, and cost.
- `tool_calls`: call/message IDs, tool name, scrubbed arguments, normalized command/path fields used by detectors.
- `tool_results`: message/call IDs, tool name, structural error flag, scrubbed content, and stable content hash.
- `events`: model/thinking changes and compactions, including token pressure and auto-run stop reason.
- `custom_state`: goal/TODO records and terminal goal status.
- `custom_messages`: broker-guard records and kind.
- `findings`: session, detector, classification, severity, evidence/provenance, details, detector-run generation, and creation time.
- `detector_runs`: session, detector, generation, status (`success|failed`), error summary, started/completed timestamps, and whether retained findings are stale after a failed rerun.

Use primary keys that make session replacement and repeated ingestion idempotent. Store scrubbed JSON only where preserving evolving Pi payloads is useful; do not duplicate raw records wholesale.

CLI defaults:

- Sessions: `~/.pi/agent/sessions`, override with `--sessions-dir`.
- Database: `${XDG_DATA_HOME:-~/.local/share}/pi-session-analyzer/sessions.db`, override with `--db`.

Planned MCP tools:

- `list_sessions(limit <= 100, cwd_filter?)`
- `session_summary(session_id)`
- `top_failures(limit <= 50, detector?, classification?, min_severity = warn)`; excludes `info` by default
- `get_conversation(session_id, anchor_message_id?, max_messages <= 100)`
- `get_message(session_id, message_id, path?)`
- `run_select(query)`

Session ID arguments accept an unambiguous prefix and return named ambiguity/not-found errors.

## Detector Requirements

### Structural detectors

- **Broker guard:** one structural warning per affected session, grouped by guard kind with source IDs.
- **Compaction pressure:** structural warning for compaction; error if a compaction/auto-run record reports provider failure. Include count and maximum pre-compaction tokens.
- **Tool-error burst:** structural warning per tool after at least three `is_error` results. Include count and rate as context, not as a calibrated anomaly score.
- **MCP failure:** structural error when a Pi `mcp_call` result has `is_error=true`; compatibility fallback when scrubbed content contains narrow, case-insensitive historical markers such as `mcp_call failed`, `MCP error`, or `fetch failed`. Details identify `structural_flag` versus `historical_text_fallback`; tests prevent broad matching of ordinary discussion about errors.

### Heuristic detectors

- **Retry loop:** the invocation key is `(tool_name, normalized target)`: trim surrounding whitespace from an exact bash command; apply `filepath.Clean` to read/edit/write paths without case-folding. Fire only when one key occurs at least four times and at least three chronologically consecutive joined results are errors with unchanged `is_error`, or have the same scrubbed-content hash. Repeated tests with changing output and a final pass must not fire.
- **Silent close:** evaluate unfinished-goal closure only when a goal was actually started. Error when the last goal state is incomplete and the final assistant stops normally; warning when a final failed tool result is followed by a normal stop. Goalless sessions are exempt.
- **Unverified code change:** evaluate the final code edit/write in a session. Treat extensions `.go`, `.js`, `.jsx`, `.ts`, `.tsx`, `.py`, `.rb`, `.rs`, `.java`, `.kt`, `.kts`, `.c`, `.h`, `.cc`, `.cpp`, `.hpp`, `.cs`, `.php`, `.swift`, `.scala`, `.sh`, `.bash`, `.zsh`, `.sql`, and `.proto`, plus basenames `Makefile` and `Dockerfile`, as code. Fire only when no later bash invocation matches an explicit verification grammar: `go test|build|vet`, `pytest`, `cargo test|check`, `make test|check|build|lint`, `npm test`, or `npm|pnpm|yarn run test|check|build|lint|typecheck` (allowing normal arguments after the recognized command). Paths under a `docs/` component and `.md`, `.txt`, `.json`, `.yaml`, `.yml`, `.toml`, `.lock`, and `.svg` files are non-code. Unknown extensions do not fire.
- **Edit without read:** normalize paths with `filepath.Clean`. Exempt a path previously passed to `read`, previously created by `write`, or previously read by a bash command. Recognize shell reads conservatively at command start or after `&&`, `;`, or `|` for `cat`, `head`, `tail`, `less`, `grep`, `rg`, `sed -n`, or `awk`, and require the command segment to contain the normalized path or its basename as a shell token. Tests cover quoted paths, basename matches, unrelated same-prefix paths, and commands that merely echo a filename.
- **Termination:** provider `stop_reason=error` is an error; user-driven `aborted` is informational and excluded from default top-failure output.

Every detector needs positive and healthy non-firing fixtures. Calibration-dependent ideas such as expensive-stall, over-review, excessive clarification, and thinking instability remain out of scope.

## Implementation Workstreams

### 1. Module scaffold and repository registration

Create `pi-session-analyzer/` with the standard module, Makefile, lint config, binary ignore, Cobra entrypoint, package skeleton, README/DESIGN/CLAUDE docs, and `AGENTS.md -> CLAUDE.md` symlink. Register it in root `Makefile` and `go.work`. Keep command code thin and business logic under `internal/`.

Likely package dependency direction:

```text
cmd -> mcp -> detect -> store -> ingest/scrub
```

Avoid cycles by keeping parsed input types in `internal/ingest`, persistence/query models in `internal/store`, and MCP response types at the handler boundary.

### 2. Typed JSONL reader and scrubber

Implement a scanner-based reader with a sufficiently large token buffer for multi-kilobyte compaction records. Parse a small type envelope first, then role/type-specific payloads. Preserve 1-based source line numbers and counts for total, skipped, unknown, and schema-drift records.

Build an ordered credential rule set covering at least GitHub, AWS, Slack, common `sk-` API keys, JWTs, PEM private keys, bearer authorization values, and explicit secret/password/token assignments. Replacements should identify the rule without retaining the value. Ensure applying the scrubber twice is stable.

### 3. Private normalized store and ingest lifecycle

Follow the existing SQLite open/migration/permissions pattern. Apply scrubbing inside the only session write path so callers cannot accidentally persist raw text. Replace all rows for one changed session in a transaction and derive stable hashes from scrubbed tool-result content.

The ingest service walks session JSONL files, skips unchanged source metadata, updates changed sessions, and never reconciles database rows against directory absence. Test deletion retention by ingesting fixtures, deleting one source, ingesting again, and querying the retained session and findings.

### 4. Summary CLI milestone

Add commands and query helpers for ingest, session lists, and detailed summaries. Keep human output concise while making each metric's meaning explicit. Do not use aggregate `totalTokens` as a work metric because cache replay can distort it. Add command tests for required arguments, ambiguous IDs, empty databases, malformed sessions, and unchanged second ingest.

### 5. Finding engine and detectors

Implement a shared detector registry and idempotent per-session recomputation. Run each `(session, detector)` independently. Compute new findings first, then in one transaction replace only that detector's prior findings and mark its `detector_runs` row `success` with a new generation. If computation fails, retain its prior findings, mark the run `failed` and retained rows stale through the run generation/status, continue other detectors, and return an aggregate error after all detectors finish. On a first-run failure there are no findings, but the failed run remains visible. CLI summaries, `detect`, and MCP responses expose failed/stale detector status; `detect` exits nonzero after reporting all failures. Tests cover a successful run followed by a failed rerun and recovery.

Implement structural detectors first, then heuristic detectors with their guard tests. Persist classification independently from severity. Wire detection into successful changed-session ingest and expose an explicit recomputation command.

### 6. MCP error propagation coverage

Extend `mcp-broker` tests to cover upstream `IsError=true` and backend call failure propagation through the broker's MCP handler. Existing denial tests remain the baseline. If these tests expose a broker defect, fix the smallest propagation path and update broker design/docs only if externally observable behavior changes.

Do not claim that this changes Pi transcript serialization. The analyzer remains compatible with both future structural Pi records and historical text-only records.

### 7. Bounded stdio MCP

Mirror the repository's MCP server/handler registration pattern. Centralize JSON response limiting and truncation so every tool emits valid JSON under caps. Conversation drill-down should use the linear source-line order, return compact message/tool summaries, and expand only requested messages.

Implement SQL execution on a separately opened read-only connection. Validate a single SELECT/CTE statement, reject obvious mutating/attachment/schema operations, execute under timeout, and cap rows/output regardless of caller SQL. Test that read attempts succeed and writes cannot alter the database even if lexical validation is bypassed in a lower-level test.

### 8. Documentation, diagram, and empirical verification

Complete tool docs according to repository document purposes:

- `pi-session-analyzer/README.md`: installation, quick start, commands, MCP registration, private-data warning, retention, and limits.
- `pi-session-analyzer/DESIGN.md`: architecture, trust boundary, data model, detector semantics/guards, design decisions, and non-goals.
- `pi-session-analyzer/CLAUDE.md`: development commands, package flow, scrub-at-write invariant, synthetic-fixtures-only rule, and token accounting gotcha.
- Root `README.md`, root `CLAUDE.md`/`AGENTS.md`, and `assets/tool-relationships.svg`: register the new tool and depict read-only Pi-log ingest, private SQLite, and stdio MCP behind the broker.

Run the real-corpus ingest and stable sampled review locally. Do not write private snippets into docs, tests, commits, or completion evidence.

## Documentation Impact

Documentation changes are required because this adds a new user-facing tool and root-level tool entry:

- Create `pi-session-analyzer/README.md`, `pi-session-analyzer/DESIGN.md`, and `pi-session-analyzer/CLAUDE.md`; symlink `pi-session-analyzer/AGENTS.md` to `CLAUDE.md`.
- Update root `README.md` overview/install/tool sections.
- Update root `CLAUDE.md` structure list; root `AGENTS.md` follows its existing relationship to that content.
- Update `assets/tool-relationships.svg` and verify both structure and rendered appearance.
- Update `mcp-broker` docs only if test-driven work changes its externally observable behavior; test-only coverage needs no user-facing broker documentation change.

## Testing / Verification

- **V1 (AC-1, AC-16):** Run `make audit` in `pi-session-analyzer/`; expect tidy, formatting, lint, race-enabled tests, and vulnerability checks to pass. Run root `make build`, `make lint`, and `make test`; expect all registered tools to pass.
- **V2 (AC-2–AC-5):** Run focused ingest/scrub/store tests with synthetic seven-type, malformed, torn-line, schema-drift, duplicate-ID, secret, unchanged-ingest, changed-ingest, and deleted-source fixtures. Inspect the test database schema to confirm no raw tier exists. Verify the analyzer leaf is `0700` and the database/WAL/SHM files are `0600`, including after forcing sidecar deletion/recreation; verify shared XDG ancestors are unchanged.
- **V3 (AC-6–AC-11):** Run CLI integration tests and focused detector suites. Each detector must have firing and healthy non-firing cases; repeated detector execution must produce identical rows.
- **V4 (AC-12–AC-14):** Run MCP handler/guard tests, including annotation completeness, valid-JSON truncation, list/drill caps, read queries, CTEs, multi-statement rejection, pragma/attachment/write rejection, timeout behavior, and immutable read-only execution.
- **V5 (AC-15):** Run focused `mcp-broker` handler/E2E tests proving structural `IsError` for denials, upstream error results, and backend call failures. Run the full broker test suite afterward.
- **V6 (AC-17):** Run `pi-session-analyzer ingest` against the local Pi corpus, then rerun unchanged. Put the tested commit, exact commands, session/line/skipped counts, and zero-changed second-run result in durable `/goal` completion evidence and the final summary; include no transcript content.
- **V7 (AC-18):** Produce findings ordered by `(detector, session_id, first_evidence_id)` and review the AC-18 bounded sample against local raw logs. Put per-detector population/sample sizes, apparent false-positive counts, and threshold changes in durable `/goal` completion evidence and the final summary; include no private content or separate tracked corpus report.
- **V8 (AC-19):** Review tool/root docs against actual command help and MCP schemas. Parse the SVG as XML, render it to a temporary PNG, inspect it with the image reader for layout defects, and retain no temporary render in the repository.
- **V9 (final audit):** Run `git status -sb` and inspect the diff by path. Confirm the current branch was modified, the research worktree remains clean, no real session fixtures or database files are tracked, and every AC has concrete evidence before goal completion.

## Risks and Mitigations

- **Historical MCP failures are format-fragile:** Keep the fallback regex narrow, identify fallback evidence in findings, and prefer structural flags whenever Pi records them.
- **Broker/Pi boundary confusion:** Tests can prove the broker emits MCP `IsError`; they cannot prove Pi persists it. State this boundary in design docs and avoid promising structural future transcripts until Pi is fixed externally.
- **Credential leakage:** Scrub in the sole write path, prohibit raw tiers and real fixtures, test marker preservation, use private file modes, and document that identifiers remain sensitive.
- **Heuristic false positives:** Separate classification from severity, require healthy counterexamples, expose evidence, and calibrate via bounded real-corpus review rather than claiming statistical validity.
- **Schema drift or damaged logs:** Skip malformed records with counters, retain valid records, surface schema drift in summaries, and keep parser fixtures for unknown types/versions.
- **Cost/token double counting:** Replace sessions transactionally, key messages by session/message ID, and report token categories rather than treating aggregate total as generated work.
- **Read-only SQL bypass:** Enforce SQLite read-only mode and query-only operation independently of lexical checks; cap execution time, rows, and serialized output.
- **Silent historical deletion:** Never reconcile absent source files during ingest; test retention explicitly and defer deletion to a deliberate future command.
- **Scope expansion into observability/reflection:** Keep visualization, exports, live tailing, rankings, config provenance, and config edits in non-goals until core usage provides evidence.

## Assumptions

- Pi continues to write local JSONL sessions under `~/.pi/agent/sessions` with enough stable envelope fields for best-effort type dispatch; unknown additions are tolerated rather than fatal.
- The tool runs under the same local user who owns the source logs and database.
- Session source size plus modification time is an adequate fast unchanged-file check because explicit `detect` can recompute findings and changed sessions are transactionally replaced.
- A simple optional dotted-field projection for `get_message` is sufficient; full JSONPath compatibility is not required.
- Real-corpus verification is available in the implementation environment but remains local and privacy-safe.

## Handoff Summary

Suggested autonomous objective:

```text
/goal Implement .plans/2026-07-11-pi-session-analyzer.md on the current branch. Do not modify the research branch or worktree. Complete only after every acceptance criterion has concrete test, command, file, documentation, or privacy-safe real-corpus evidence.
```

Implement the cgo-free analyzer in milestone order: scaffold and parser/scrubber/store, summary CLI, classified structural and guarded heuristic detectors, broker propagation coverage, bounded read-only MCP, then docs/diagram and real-corpus review. Treat the plan as intent and constraints rather than a prescribed diff. Do not broaden scope into visualization or self-modifying configuration work.
