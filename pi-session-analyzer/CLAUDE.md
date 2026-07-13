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
go test ./internal/mcp ./internal/robound
make test-frontend
```

## Package Flow

```text
cmd/pi-session-analyzer -> internal/app, internal/mcp, internal/dashboard
internal/app            -> internal/detect, internal/store, internal/ingest
internal/dashboard      -> internal/store, internal/robound, internal/detect (registry metadata only)
internal/mcp            -> internal/store, internal/robound
internal/store          -> internal/ingest, internal/scrub, internal/outcome
internal/detect         -> internal/ingest, internal/outcome
```

Keep Cobra, MCP registration, and HTTP handlers thin. Parsing belongs in `internal/ingest`, persistence and typed query semantics in `internal/store`, detector logic in `internal/detect`, lifecycle orchestration in `internal/app`, protocol-neutral read/cap limits in `internal/robound`, and rendering/state in `internal/dashboard`. Dashboard assets are embedded, dependency-free ES modules; frontend tests live outside the asset glob in `internal/dashboard/frontend_test`.

## Invariants

- `Store.ReplaceSession` is the sole normalized session write boundary. Scrub every new free-text or JSON field there before persistence; never add a raw tier.
- Parser types must not retain assistant thinking content or `thinkingSignature`. Reasoning usage counts are allowed.
- Tests and fixtures must be synthetic. Never commit real Pi records, databases, transcript snippets, or corpus review output.
- Preserve source-line provenance and primary-key deduplication when adding record support.
- Ingest is update/additive only. Missing source files do not imply deletion.
- Keep detector classification (`structural|heuristic`) independent from severity (`error|warn|info`) and retain evidence IDs/details.
- A detector failure retains stale prior findings and must not prevent other detectors from running.
- MCP tools and dashboard endpoints remain read-only and closed-world. Route every response through the shared cap and dedicated `mode=ro` + `query_only` connection; keep row/value/time limits finite. Dashboard code must never call writable `store.Open`, expose SQL, create/chmod/migrate a database, or accept a non-loopback host.
- Dashboard APIs are typed, allow-listed, bounded, and parameterized. Keep fresh findings and stale evidence in separate response fields; preserve exact call-result outcome semantics and explicit unknown coverage.
- Render stored strings only with `textContent`/text nodes, never `innerHTML`. Keep assets same-origin with no browser storage, telemetry, external requests, or share/export affordances; the `HttpOnly` auth session cookie is the sole permitted cookie. Collapsed content must load bounded detail explicitly.
- Dashboard auth mirrors `mcp-broker`: 32-byte hex token (64 chars) at `0600` under `0750` config dirs, `crypto/subtle.ConstantTimeCompare` everywhere, `pi-session-analyzer-auth` cookie (`HttpOnly`, `SameSite=Strict`), and a redirect that strips `?token=` after the cookie exchange. Never print or log the token value; the loopback bind stays the load-bearing boundary.
- Preserve URL/history state only for non-sensitive filters and selection. Maintain keyboard operation, visible focus, textual chart equivalents, explicit loading/empty/error/truncated states, and responsive layouts.
- Report output, reasoning, cache-read, and cache-write usage separately in every view. Provider input is separately labeled and never folded into generated work.

The `//nolint:gosec` directives on the caller-selected database/source paths are intentional: these are the explicit local paths the CLI is designed to open. The directory-mode directive preserves the stricter `0700` trust boundary.
