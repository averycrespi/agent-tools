# pi-session-analyzer

`pi-session-analyzer` is a local, passive index and diagnostic tool for Pi coding-agent JSONL sessions. It parses Pi-specific state, stores only credential-scrubbed normalized data in private SQLite storage, reports deterministic findings, and serves bounded read-only MCP queries.

The database preserves paths, usernames, hostnames, URLs, prompts, responses, and code after credential-value scrubbing. It is private and **not safe to share**.

## Install

```bash
make install
```

Or from the repository root:

```bash
make install
```

The binary is installed as `pi-session-analyzer`.

## Quick Start

```bash
# Index new or changed sessions and run detectors for those sessions
pi-session-analyzer ingest

# Inspect the index
pi-session-analyzer list-sessions
pi-session-analyzer session-summary SESSION_ID

# Recompute every detector, or only one exact/unique session ID prefix
pi-session-analyzer detect
pi-session-analyzer detect SESSION_ID
```

Defaults are XDG-aware:

- Source sessions: `~/.pi/agent/sessions`; override with `--sessions-dir`.
- Database: `${XDG_DATA_HOME:-~/.local/share}/pi-session-analyzer/sessions.db`; override with `--db`.

The analyzer-owned database directory is mode `0700`. The database and SQLite sidecars are repaired to `0600`. Ingestion is additive/update-only: deleting a source JSONL file does not delete its indexed session. There is no implicit prune command.

## Commands

- `ingest` tolerantly parses all known Pi record types, records malformed/unknown/schema-drift counts, transactionally replaces changed sessions, skips unchanged files, and recomputes findings only for changed sessions.
- `list-sessions [--limit 1..100] [--cwd FILTER]` returns compact indexed session rows.
- `session-summary SESSION_ID` reports cost; output, reasoning, cache-read, and cache-write usage; tool calls/errors; stop and goal state; compactions; broker guards; schema drift; findings; and detector freshness. It intentionally does not present `totalTokens` as generated work.
- `detect [SESSION_ID]` independently and idempotently reruns all detectors. A failed detector retains prior findings as stale while other detectors continue.
- `mcp` serves read-only stdio MCP tools.

Session IDs may be exact or unambiguous prefixes.

## Detectors

Every finding separately records a fixed `error`, `warn`, or `info` severity and one of these evidence classifications:

- **Structural:** broker guards, compaction/provider failures, tool-error bursts, and MCP failures. MCP errors prefer structural `is_error`; narrow text markers support historical Pi logs that discarded the flag.
- **Heuristic:** retry loops, incomplete-goal silent closes, unverified code changes, edit-before-read behavior, and provider/user termination states. These are guarded inferences, never presented as structural facts.

Findings include detector identity, evidence IDs, source-line provenance, and machine-readable details. Detector thresholds are deterministic, not statistically calibrated scores.

## MCP Registration

Run the analyzer behind `mcp-broker` so calls are subject to broker policy and auditing. Add a stdio backend to the broker configuration:

```json
{
  "servers": {
    "pi-sessions": {
      "command": "pi-session-analyzer",
      "args": ["mcp"]
    }
  }
}
```

The server exposes `list_sessions`, `session_summary`, `top_failures`, `get_conversation`, `get_message`, and `run_select`. Every tool is annotated read-only, non-destructive, and closed-world.

Limits are enforced centrally: list/drill-down requests are bounded, SQL returns at most 1,024 rows, individual SQL values are limited to 64 KiB, serialized responses are approximately 50,000 characters, and SQL has a five-second timeout. `run_select` accepts one `SELECT` or CTE and executes through a separate SQLite `mode=ro` connection with `query_only` enabled.

Broker discovery and policy are not authorization to share analyzer output. The same private/non-share-safe boundary applies to MCP responses.

## Scrubbing and Retention

The credential scrubber covers known token/API-key formats, authorization values, explicit password/secret/token assignments, JWTs, and private-key blocks. It deliberately preserves diagnostic markers and does not redact identifiers. Raw records, assistant thinking content, and `thinkingSignature` are never stored. Reasoning token counts remain available.

The original Pi JSONL logs remain the raw source of record. The SQLite index remains queryable after a source disappears and must be deleted explicitly by the local user when no longer needed.

## Non-Goals

This release is not a live proxy, file watcher, browser dashboard, shareable export/report format, remote or multi-user service, authentication layer, cost analytics product, probabilistic ranking system, configuration cohort system, or autonomous configuration-editing loop. It does not fix Pi's external MCP-response-to-transcript serialization.
