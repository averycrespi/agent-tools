# pi-session-analyzer

Local passive Pi session indexing, deterministic diagnostics, and bounded read-only MCP.

## Development

```bash
make build
make test
make lint
make audit
```

Focused packages:

```bash
go test ./internal/ingest ./internal/scrub ./internal/store
go test ./internal/detect ./internal/app
go test ./internal/mcp
```

## Package Flow

```text
cmd/pi-session-analyzer -> internal/app, internal/mcp
internal/app            -> internal/detect, internal/store, internal/ingest
internal/mcp            -> internal/store
internal/store          -> internal/ingest, internal/scrub
internal/detect         -> internal/ingest
```

Keep Cobra and MCP registration thin. Parsing belongs in `internal/ingest`, all persistence/query behavior in `internal/store`, detector logic in `internal/detect`, lifecycle orchestration in `internal/app`, and protocol bounds in `internal/mcp`.

## Invariants

- `Store.ReplaceSession` is the sole normalized session write boundary. Scrub every new free-text or JSON field there before persistence; never add a raw tier.
- Parser types must not retain assistant thinking content or `thinkingSignature`. Reasoning usage counts are allowed.
- Tests and fixtures must be synthetic. Never commit real Pi records, databases, transcript snippets, or corpus review output.
- Preserve source-line provenance and primary-key deduplication when adding record support.
- Ingest is update/additive only. Missing source files do not imply deletion.
- Keep detector classification (`structural|heuristic`) independent from severity (`error|warn|info`) and retain evidence IDs/details.
- A detector failure retains stale prior findings and must not prevent other detectors from running.
- MCP tools remain read-only and closed-world. Route every response through the central cap; keep SQL row/time limits and the separate `mode=ro` plus `query_only` boundary.
- Report output, reasoning, and cache usage separately. Do not describe `totalTokens` as generated work.

The `//nolint:gosec` directives on the caller-selected database/source paths are intentional: these are the explicit local paths the CLI is designed to open. The directory-mode directive preserves the stricter `0700` trust boundary.
