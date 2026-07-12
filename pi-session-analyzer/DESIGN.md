# Design

## Purpose

`pi-session-analyzer` provides retrospective, local-only diagnostics for Pi coding-agent sessions. Generic provider traces omit Pi-specific state such as goal/TODO snapshots, broker guards, compactions, tool results, and stop reasons. The analyzer preserves those signals without joining Pi's live provider or MCP critical path.

## Architecture

```text
Pi JSONL files
  -> tolerant typed parser
  -> normalized in-memory session (no thinking/signatures)
  -> sole scrub-at-write SQLite boundary
  -> deterministic detector registry
  -> CLI summaries
  -> shared bounded read-only capability
     -> stdio MCP
     -> loopback-only embedded dashboard
```

The module is cgo-free and uses `github.com/ncruces/go-sqlite3`. Command flow is `cmd -> app/mcp/dashboard`; MCP and dashboard use typed store readers over `internal/robound`, while writable ingest/detection remains in `app -> store`. The source logs remain the raw source of record; there is no parallel raw database tier.

## Trust Boundary

The supported deployment is one local user. The analyzer leaf directory is `0700`; the database and recreated WAL/SHM sidecars are `0600`. Shared XDG ancestors are not chmodded. Every free-text or JSON value passes through the credential scrubber in the transactional store boundary.

Scrubbing targets credential values, including known GitHub/AWS/Slack/API token forms, JWTs, authorization headers, explicit secret/password/token assignments, and PEM private keys. It intentionally retains identifiers such as paths, usernames, hosts, and URLs. Consequently, the database, CLI output, and MCP output are private and non-share-safe.

Assistant thinking blocks and `thinkingSignature` are discarded during typed parsing. Reasoning usage counts are retained. No real session line is a test fixture.

## Ingestion and Data Model

The scanner accepts large records and dispatches the seven known top-level Pi types: `session`, `message`, `model_change`, `thinking_level_change`, `compaction`, `custom`, and `custom_message`. Unknown types and schema versions are counted. Malformed middle records and torn final lines are skipped without discarding valid records from the file. Every normalized item retains a 1-based source line.

The normalized tables are `sessions`, `messages`, `tool_calls`, `tool_results`, `events`, `custom_state`, `custom_messages`, `findings`, and `detector_runs`. Session/message/call/result IDs enforce deduplication. A changed source is transactionally replaced; unchanged `(path, size, mtime)` input is skipped. Ingest never reconciles absent files, so indexed data remains available after source deletion.

Token reporting keeps output, reasoning, cache-read, and cache-write categories separate. Provider-reported input may appear separately in message drilldown. Aggregate `totalTokens` is not persisted or presented as generated work because cache replay can dominate it. Parseable session-start text is also stored as an indexed nullable canonical Unix instant; the idempotent writable migration backfills existing parseable rows, while invalid timestamps remain explicit untimed sessions.

## Detector Semantics

Classification and severity are independent persisted fields. `structural` means explicit record/error evidence; `heuristic` means a guarded sequence inference. Severity is fixed to `error`, `warn`, or `info`. Every finding carries detector identity, evidence ID, source line, JSON details, generation, and stale state.

Structural detectors cover:

- broker guards grouped by kind;
- compaction pressure and compaction/provider failure;
- three-or-more trailing structural tool errors per tool without a later successful recovery;
- MCP failure from `is_error`, with a narrow case-insensitive historical marker fallback that names its evidence source.

Heuristic detectors cover:

- four-or-more repeated normalized invocations with a terminal failure and a qualifying consecutive/same-result failure run;
- normal closure after a started but incomplete goal (goalless sessions are exempt);
- a final recognized code edit without a later explicit test/build/check/lint command;
- edit of an existing path without a prior direct or conservative shell read (newly written paths are exempt);
- provider errors and informational user cancellation.

The code-change grammar excludes docs/config-only and unknown-extension edits. Shell reads are recognized only at command or conservative control-flow starts for a small allowlist, canonicalized against the session working directory, and require an exact normalized path or basename token. Findings are deterministic diagnostics, not precision/recall claims or calibrated anomaly scores.

Each detector runs independently. Success atomically replaces only that detector's findings and advances its generation. Failure retains prior findings as stale, records a failed run, continues the registry, and causes the caller to return an aggregate error. Recovery replaces stale rows with fresh output.

## Dashboard

`dashboard` serves one embedded, dependency-free HTML/CSS/ES-module application on literal `127.0.0.1`; port `0` is ephemeral and there is no host override. The printed local URL is authoritative. Browser opening is optional convenience and launch failure is non-fatal. The dashboard never initializes, migrates, chmods, or writes the database.

The browser receives only typed, parameterized, bounded view models. The default range is 30 calendar days in a validated labeled IANA timezone; 7/30/90/all-history and explicit half-open dates are supported. `auto` resolves to day through 90 days, week through 18 months, month while within the 90-bucket bound, then year for longer histories. Go computes Monday-based calendar boundaries with IANA timezone rules; invalid starts are counted as untimed. Empty and current partial buckets remain explicit.

Overview panels cover sessions, logged cost, call volume and classifiable per-tool errors, separate token categories, compactions, broker guards, current findings, detector gaps, goals, final stop reasons, and record/turn distributions. A sortable, keyset-paged session signal matrix bridges selected buckets to drilldown. The linear drilldown pages every record kind by `(source_line, kind_rank, id)`, lazily retrieves bounded collapsed text, anchors finding evidence exactly, and separately presents token sequence/compactions, tool coverage, current findings, stale retained evidence, detector status, goal progression, and tolerant TODO progression/final state.

Tool calls are classified once by exact `(session_id, call_id)`: confirmed error wins, then the narrow detector-approved inferred MCP fallback, then explicit success, otherwise unknown. Rates are `(confirmed + inferred) / classifiable` and remain null without coverage. Orphans, multiple results, and name mismatches are data quality. Current findings require a successful current detector run at the same generation; failed/not-run coverage is explicit and stale evidence is never placed in a current collection.

All assets and requests are same-origin. CSP, frame/referrer/type protections and `Cache-Control: no-store` apply to every response. There are no cookies, browser persistence, external assets, telemetry, export/share/download/print controls, redaction claims, raw tier, arbitrary SQL, polling, or live tailing. Stored values are inserted only as text nodes. A persistent warning states that retained scrubbed content is private and not safe to share or screenshot.

## Shared Read Safety and MCP

The stdio server exposes six closed-world read-only tools: `list_sessions`, `session_summary`, `top_failures`, `get_conversation`, `get_message`, and `run_select`. All registry entries explicitly advertise read-only, non-destructive, idempotent, closed-world annotations.

One protocol-neutral serializer in `internal/robound` caps every MCP and dashboard success/error response at approximately 50,000 bytes while preserving valid JSON. Lists and drill-downs have tool-specific maximums. SQL has an independent 1,024-row cap, a 64 KiB SQLite value limit, and a five-second context timeout.

`run_select` validates a single `SELECT` or CTE and rejects statement separators and obvious write/schema/attachment operations. Those lexical checks are defense in depth. The write-safety boundary is a separate SQLite `mode=ro` connection with `PRAGMA query_only=ON`; caller SQL cannot make the analyzer connection writable.

## MCP Error Boundary

The broker preserves upstream `CallToolResult.IsError`, and turns broker denials and backend call failures into structural MCP error results. E2E tests cover all three paths. Historical Pi transcripts may still omit that flag because Pi's response-to-transcript serialization is external to this repository. The analyzer therefore prefers structural data and retains a labeled historical text fallback.

## Non-Goals

V1 excludes export/share/download formats, present-safe redaction, live tailing or polling, remote/multi-user operation, authentication/TLS, telemetry, external assets, autonomous pruning, cost-product analytics, budgets/forecasts, statistical rankings, configuration fingerprints/cohorts, configuration proposals or edits, OpenInference/Phoenix export, arbitrary/custom dashboards, browser SQL, duration/latency waterfalls, branch/tree views, and any self-modifying reflection loop.
