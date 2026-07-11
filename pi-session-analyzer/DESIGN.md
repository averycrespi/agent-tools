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
  -> CLI summaries and bounded read-only stdio MCP
```

The module is cgo-free and uses `github.com/ncruces/go-sqlite3`. Package flow is `cmd -> app/mcp/detect -> store -> ingest/scrub`. The source logs remain the raw source of record; there is no parallel raw database tier.

## Trust Boundary

The supported deployment is one local user. The analyzer leaf directory is `0700`; the database and recreated WAL/SHM sidecars are `0600`. Shared XDG ancestors are not chmodded. Every free-text or JSON value passes through the credential scrubber in the transactional store boundary.

Scrubbing targets credential values, including known GitHub/AWS/Slack/API token forms, JWTs, authorization headers, explicit secret/password/token assignments, and PEM private keys. It intentionally retains identifiers such as paths, usernames, hosts, and URLs. Consequently, the database, CLI output, and MCP output are private and non-share-safe.

Assistant thinking blocks and `thinkingSignature` are discarded during typed parsing. Reasoning usage counts are retained. No real session line is a test fixture.

## Ingestion and Data Model

The scanner accepts large records and dispatches the seven known top-level Pi types: `session`, `message`, `model_change`, `thinking_level_change`, `compaction`, `custom`, and `custom_message`. Unknown types and schema versions are counted. Malformed middle records and torn final lines are skipped without discarding valid records from the file. Every normalized item retains a 1-based source line.

The normalized tables are `sessions`, `messages`, `tool_calls`, `tool_results`, `events`, `custom_state`, `custom_messages`, `findings`, and `detector_runs`. Session/message/call/result IDs enforce deduplication. A changed source is transactionally replaced; unchanged `(path, size, mtime)` input is skipped. Ingest never reconciles absent files, so indexed data remains available after source deletion.

Token reporting keeps output, reasoning, cache-read, and cache-write categories separate. Aggregate `totalTokens` is not persisted or presented as generated work because cache replay can dominate it.

## Detector Semantics

Classification and severity are independent persisted fields. `structural` means explicit record/error evidence; `heuristic` means a guarded sequence inference. Severity is fixed to `error`, `warn`, or `info`. Every finding carries detector identity, evidence ID, source line, JSON details, generation, and stale state.

Structural detectors cover:

- broker guards grouped by kind;
- compaction pressure and compaction/provider failure;
- three-or-more structural tool errors per tool;
- MCP failure from `is_error`, with a narrow case-insensitive historical marker fallback that names its evidence source.

Heuristic detectors cover:

- four-or-more repeated normalized invocations with a terminal failure and a qualifying consecutive/same-result failure run;
- normal closure after a started but incomplete goal (goalless sessions are exempt);
- a final recognized code edit without a later explicit test/build/check/lint command;
- edit of an existing path without a prior direct or conservative shell read (newly written paths are exempt);
- provider errors and informational user cancellation.

The code-change grammar excludes docs/config-only and unknown-extension edits. Shell reads are recognized only at command-segment starts for a small allowlist and require an exact normalized path or basename token. Findings are deterministic diagnostics, not precision/recall claims or calibrated anomaly scores.

Each detector runs independently. Success atomically replaces only that detector's findings and advances its generation. Failure retains prior findings as stale, records a failed run, continues the registry, and causes the caller to return an aggregate error. Recovery replaces stale rows with fresh output.

## MCP Safety and Bounds

The stdio server exposes six closed-world read-only tools: `list_sessions`, `session_summary`, `top_failures`, `get_conversation`, `get_message`, and `run_select`. All registry entries explicitly advertise read-only, non-destructive, idempotent, closed-world annotations.

One serializer caps every success and error response at approximately 50,000 bytes while preserving valid JSON. Lists and drill-downs have tool-specific maximums. SQL has an independent 1,024-row cap and five-second context timeout.

`run_select` validates a single `SELECT` or CTE and rejects statement separators and obvious write/schema/attachment operations. Those lexical checks are defense in depth. The write-safety boundary is a separate SQLite `mode=ro` connection with `PRAGMA query_only=ON`; caller SQL cannot make the analyzer connection writable.

## MCP Error Boundary

The broker preserves upstream `CallToolResult.IsError`, and turns broker denials and backend call failures into structural MCP error results. E2E tests cover all three paths. Historical Pi transcripts may still omit that flag because Pi's response-to-transcript serialization is external to this repository. The analyzer therefore prefers structural data and retains a labeled historical text fallback.

## Non-Goals

V1 excludes browser visualization, transcript timelines, export/share formats, live tailing, remote/multi-user operation, authentication, identifier redaction, autonomous pruning, cost-product analytics, statistical rankings, configuration fingerprints/cohorts, configuration proposals or edits, OpenInference export, and any self-modifying reflection loop.
