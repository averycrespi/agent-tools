# pi-session-analyzer Dashboard Plan

## Goal

Add a private, local, view-only dashboard to `pi-session-analyzer` that makes cross-session trends and single-session diagnostics visually explorable without weakening the analyzer's scrubbed, bounded, read-only trust boundary. The dashboard must expose Pi-specific signals honestly: separate token categories, classifiable tool-error coverage, compactions, broker guards, fresh detector findings, goal/TODO progression, and linear source-ordered evidence.

## Background / Repo Context

- `.designs/2026-07-11-pi-session-analyzer-dashboard.md` contains the prior-art survey and initial architecture. The clarification decisions recorded on 2026-07-12 supersede its open questions and a few initial details; update the design before or with implementation so it remains authoritative.
- `pi-session-analyzer/DESIGN.md` currently excludes browser visualization and transcript timelines. This feature deliberately narrows that non-goal while retaining no export/share formats, remote access, authentication, telemetry, live tailing, writes, or raw-data tier.
- `pi-session-analyzer/internal/store/store.go` owns the scrubbed SQLite schema. `store.Open` creates/initializes/chmods the database and is therefore not an acceptable dashboard constructor.
- `pi-session-analyzer/internal/mcp/sql.go` currently contains the `mode=ro`, `PRAGMA query_only=ON`, 64 KiB SQLite value limit, five-second timeout, and raw-select execution. `internal/mcp/handler.go` contains the 50,000-byte capped JSON serializer. These protocol-neutral protections need one shared intra-module home rather than a weaker dashboard copy.
- The existing MCP command opens the normal writable `store.Store` for named tools and uses the read-only connection only for `run_select`. The dashboard work should finish the intended boundary: both MCP and dashboard read methods use the shared read-only connection; MCP keeps its own raw-SQL validation and protocol adapters.
- The store has session start timestamps, source-line ordering, message usage/cost, tri-state tool-result errors, events, custom goal/TODO state JSON, broker-guard messages, findings, and detector-run status/generation. It does not have reliable session/span durations or branching, so no duration waterfall, latency, or tree visualization is valid.
- The established dependency flow should become:

  ```text
  cmd/pi-session-analyzer -> internal/app, internal/mcp, internal/dashboard
  internal/dashboard      -> internal/store, internal/robound
  internal/mcp            -> internal/store, internal/robound
  internal/store          -> internal/ingest, internal/scrub
  ```

- Use synthetic fixtures only. Real Pi transcripts are private and must never enter tests, screenshots, or committed artifacts.

## Acceptance Criteria

- **AC-1 — Local command and lifecycle:** `pi-session-analyzer dashboard` binds `127.0.0.1` on an ephemeral port by default, prints the actual URL, opens the default browser unless `--no-open` is set, supports a validated optional `--port`, shuts down cleanly on interruption, and never offers or accepts a non-loopback host.
- **AC-2 — Shared read-only boundary:** MCP named reads, MCP `run_select`, and every dashboard data endpoint use the same shared `mode=ro` + `query_only` connection setup, SQLite value limit, finite query timeout, and capped JSON serialization. The dashboard cannot create, initialize, chmod, or mutate a database and exposes no SQL endpoint.
- **AC-3 — Private HTTP posture:** all assets are embedded and same-origin; there are no external requests, telemetry, cookies, export/share/download controls, permissive CORS, or raw tier. Responses use appropriate CSP/frame/referrer/content-type protections; data responses use `Cache-Control: no-store`. Stored text is inserted only as text, never executable HTML. A persistent warning says the dashboard is private and not safe to share or screenshot.
- **AC-4 — Bounded, typed APIs:** fixed parameterized endpoints cover overview trends, the session signal matrix, and session drilldown. Inputs have allow-listed filters, bounded date ranges/bucket counts/page sizes, validated IANA timezones, stable cursors, and consistent JSON error/truncation shapes. No endpoint requires loading the full database into the browser.
- **AC-5 — Honest time model:** cross-session x-axes use only canonical parsed session-start instants and half-open calendar buckets in the selected timezone. Ingest preserves the original timestamp text but also records a nullable canonical Unix instant; an idempotent writable-store migration backfills parseable existing rows and indexes the canonical column. The default range is 30 days; 7/30/90/all and explicit dates are available. `auto` resolves visibly to day/week/month with manual override. DST transitions are correct, the current partial bucket is marked, empty buckets render as zeroes, and invalid/missing timestamps are counted as untimed rather than silently assigned or discarded.
- **AC-6 — Required overview series:** the overview renders sessions started; logged cost; tool-call volume; per-tool classifiable error rate and coverage; output, reasoning, cache-read, and cache-write token panels; compaction and broker-guard counts; fresh findings by detector/severity/classification; detector coverage gaps; goal outcomes; stop-reason mix; and session record/turn-count distribution. Incommensurable units are not stacked or placed on dual axes.
- **AC-7 — Token and cost honesty:** output, reasoning, cache-read, and cache-write remain separate in every aggregate, chart, tile, tooltip, and accessible table. Provider-reported input tokens may appear separately in message drilldown but are never folded into a generated-work total. Cost is summed once from deduplicated message rows, labeled "as logged by Pi," and has no forecasts or budgets.
- **AC-8 — Error semantics:** each tool call is classified once as confirmed error, inferred error, observed success, or unknown. Explicit `is_error=true/false` is authoritative; only the existing detector-approved narrow labeled-text fallback may turn an otherwise-unlabeled result into an inferred error. Calls with no classifiable result remain unknown; orphan/ambiguous result records are exposed as data-quality counts rather than inflating a rate. Displayed rate is `(confirmed + inferred errors) / classifiable calls`, is null when no calls are classifiable, and always appears with classifiable/total coverage and the confirmed/inferred split.
- **AC-9 — Freshness honesty:** aggregate finding counts include only non-stale findings whose detector's current run succeeded at the same generation. Failed or not-run detectors create explicit coverage-gap markers even when no prior finding exists. Stale retained evidence appears only in a separately labeled drilldown section with detector, generation, and failure summary; frontend code never receives stale findings in a collection named or rendered as current.
- **AC-10 — Session signal matrix and linkage:** a bounded, sortable, accessible session table/matrix shows start, project/cwd, record/turn counts, split tokens, error/unknown coverage, compactions, broker guards, fresh finding severity, detector coverage, goal outcome, TODO outcome, stop reason, and schema quality. Clicking a multi-session chart bucket filters this matrix; a one-session bucket may open directly; selecting a row opens that session's drilldown. Filter/range/bucket/timezone/selection state is represented in the local URL and works with browser history.
- **AC-11 — Linear drilldown:** drilldown presents header metrics and one source-line-ordered paginated stream containing messages, tool calls/results, events, goal/TODO state, broker guards, and compactions. Transcript, tool argument/result, and TODO text are collapsed by default and expand only on explicit action. No branch/tree, wall-clock spacing, duration bars, or raw thinking/signatures are shown.
- **AC-12 — Drilldown diagnostics:** drilldown includes per-tool outcome/coverage, per-message split-token sequence panels with compaction markers, fresh findings with evidence ID/source-line navigation, separately labeled stale evidence, detector status, goal-state progression, TODO status-count progression, and the final TODO list. TODO parsing tolerates absent, cleared, malformed, reopened, removed, and duplicate-streamed snapshots without inventing productivity scores.
- **AC-13 — Accessibility and resilient states:** charts have textual/table equivalents or accessible summaries, keyboard-operable bucket/session/expansion controls, visible focus, semantic labels, non-color status cues, and contrast suitable for WCAG AA. Loading, empty index, empty range, invalid timestamp, capped/truncated response, missing database, stale index, query failure, and detector failure states are visibly distinct and actionable without writes.
- **AC-14 — Documentation alignment:** the companion design records all clarified decisions, including adaptive bucketing, browser-local labeled timezone, no redaction mode, vanilla SVG, classifiable error semantics, TODO progression, stale exclusion, session matrix, and bespoke-only/no-Phoenix V1. `DESIGN.md`, `README.md`, and `CLAUDE.md` accurately describe the command, architecture, privacy boundary, package flow, and remaining non-goals.
- **AC-15 — Verification:** focused and full Go tests, lint, build, dependency audit, frontend unit tests, and a browser smoke/visual inspection pass all succeed. Verification proves loopback/read-only enforcement, cap behavior, honest aggregation edge cases, safe text rendering, keyboard navigation, and bucket-to-matrix-to-drilldown flow.

## Non-Goals / Out of Scope

- Phoenix/OpenInference or any other export/integration.
- Static HTML/JSON generation, share links, downloads, print/share affordances, or a present-safe/redaction mode.
- Remote/multi-user serving, authentication, TLS, telemetry, external fonts/scripts/styles, or cloud services.
- Writes, ingest, detector execution, config changes, remediation actions, file watching, polling, or live tailing from the dashboard.
- Browser-issued SQL, arbitrary chart builders, saved dashboards, or a second raw/unscrubbed query tier.
- Duration/latency/TTFT metrics, Gantt/waterfall duration bars, branch/tree views, correction rates, cache-hit ratios, productivity scores, config cohorts, budgets, forecasts, funnels, or retention analytics.
- Calendar activity heatmap, model-mix board, and separate generic top-failures board in V1; the clarified broadening is the session signal matrix. Existing required panels may still expose model/failure context where it directly explains a session.
- New MCP aggregate tools in V1.
- A new Go module/tool or changes to root `TOOLS`, `go.work`, or `assets/tool-relationships.svg`.

## Constraints

- Preserve cgo-free SQLite through `ncruces/go-sqlite3`; prefer Go standard-library HTTP, embed, URL, time, signal, and subprocess support.
- Keep Cobra registration and HTTP handlers thin; metric semantics and SQL belong in typed store/read-service code, not JavaScript.
- Use a no-build frontend: one embedded HTML shell, CSS, and small ES modules using vanilla JavaScript and SVG. Add no frontend framework or chart dependency.
- Bind only to literal IPv4 loopback (`127.0.0.1`) in V1. `--port 0` chooses an ephemeral port; valid explicit ports are `1..65535`. There is no `--host` or `--addr` escape hatch.
- Cap successful and error JSON bodies with the shared serializer. Endpoint-level pagination and aggregation should normally stay below the cap; truncation is a last-resort safety state the frontend must handle, not normal pagination.
- Use finite HTTP `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`; propagate request cancellation into query contexts.
- All filter values are parsed/validated server-side. Use parameterized SQL only; dynamic sort/group choices map through allow-listed server constants.
- Compute IANA calendar bucket boundaries in Go, then aggregate against explicit half-open canonical Unix instants. Do not compare raw RFC3339 text or depend on SQLite's local-time rules. Bound generated buckets (for example, at 400); `auto` coarsens all-history ranges as needed.
- Add a nullable canonical session-start instant and its range index through an idempotent writable `store.Open` migration. Preserve the raw timestamp for provenance. Backfill parseable existing rows in Go using the same parser used at ingest; invalid values remain null. `ReplaceSession` populates both forms. A dashboard opened against a pre-migration schema must fail clearly with an instruction to run `ingest`; the dashboard itself never migrates.
- Render all stored strings through DOM `textContent`/text nodes. Collapsing content is a UX default, not sanitization or redaction.

## Chosen Approach

Implement an embedded-asset HTTP dashboard as a `dashboard` subcommand in the existing `pi-session-analyzer` binary. Extract protocol-neutral read-only SQLite setup and capped JSON encoding from `internal/mcp` into `internal/robound`; expose typed store readers over the resulting dedicated read-only connection; migrate MCP named reads and `run_select` to this boundary; and place fixed dashboard queries in `internal/store`.

The browser receives bounded view models rather than rows or SQL capability. Go owns filtering, calendar boundaries, aggregation, freshness separation, error classification, pagination, and safe response shapes. A small client-side ES-module application renders accessible SVG plus HTML tables, keeps local view state in the URL, and requests detail only when selected or expanded.

This bespoke path is preferred over Phoenix because the differentiating views—goals/TODOs, compactions, broker guards, detector freshness, evidence provenance, and honest Pi token/error semantics—are not generic spans, while export would create a second private-data copy and add a service/dependency footprint. Static generation is rejected because it is an export artifact and goes stale; browser `run_select` is rejected because it cannot enforce semantic honesty and would pressure the central caps.

## Design Decisions

- **D1 — Existing subcommand:** command flow is `cmd -> internal/dashboard -> internal/store + internal/robound`; no new tool/module.
- **D2 — Shared read capability:** `internal/robound` owns a long-lived dedicated read-only SQLite connection and protocol-neutral capped JSON. `internal/mcp` retains SELECT/CTE lexical policy, row shaping, MCP annotations, and MCP result conversion.
- **D3 — One call, one error outcome:** associate results to calls only by exact `(session_id, call_id = tool_call.id)`; never associate by tool name. The existing result-name/call-name fallback labels an already-associated outcome only. Collapse zero or more linked results deterministically per call: confirmed error wins, then approved inferred error, then explicit success, otherwise unknown. A result with no exact call is orphaned; inconsistent result names and multiple linked results are data-quality counts. Preserve confirmed/inferred/unknown counts and exclude orphan records from rates.
- **D4 — Calendar boundaries, not fixed durations:** browser supplies a validated IANA timezone; server computes day/week/month boundaries with Go's timezone database and aggregates over corresponding instants. Week boundaries start Monday. The response echoes timezone and resolved bucket.
- **D5 — Adaptive default:** resolve `auto` to day for ranges through 90 days, week through 18 months, and month beyond that, coarsening further if needed to remain under the bucket bound. Users can select day/week/month where the resulting bucket count remains within the limit.
- **D6 — Invalid timestamp policy:** temporal charts exclude unparseable session starts and return an explicit untimed-session count. The matrix can expose those sessions through an `untimed` filter so they remain diagnosable.
- **D7 — Fresh collection separation:** store/view-model APIs return fresh findings and stale evidence as different fields/types. Detector coverage is computed from the current detector registry plus `detector_runs`, not inferred from finding rows.
- **D8 — TODO is typed at the read boundary:** codify the Pi `todo-state` snapshot contract as `{items:[{id,text,status,notes?}]}` with statuses `todo`, `in_progress`, `done`, and `blocked`, using synthetic parser/store fixtures. Parse only that custom type. Distinct custom-entry IDs are progression snapshots; a later record with the same ID is a streamed correction that replaces the earlier copy under the parser's existing upsert rule and is not counted as a second transition. Preserve malformed snapshots as data-quality state. Existing indexes already retain the latest scrubbed JSON/source line for each ID and need no schema migration; history overwritten by a producer-reused ID is unrecoverable and must not be invented. Progression is status counts per retained source line and final item state, not inferred task effectiveness.
- **D9 — Matrix is the selection bridge:** chart buckets filter the paginated matrix; rows link to drilldown. This replaces the design's proposed calendar heatmap as the one broadened V1 overview.
- **D10 — No-build SVG:** use reusable vanilla-JS primitives for bars, lines/sparklines, dot distributions, token sequence marks, legends, tooltips, and keyboard selection. Every chart also exposes an accessible summary/table; no uPlot fallback is part of V1.
- **D11 — Private by construction:** no host option, no static export, no external asset, no CORS, no cookie, no telemetry, no HTML interpretation of stored data, and no redaction claim.
- **D12 — Browser opening is convenience only:** failure to launch the browser is non-fatal after the URL is printed; the server remains usable. `--no-open` makes this deterministic for tests/headless use.

## Implementation Notes

### Shared read boundary and store reader

- Add `pi-session-analyzer/internal/robound/` for:
  - read-only DSN construction and database-existence errors;
  - one owned `*sql.Conn` configured with `query_only`, SQLite value limit, and finite per-operation contexts;
  - a narrow query/exec interface usable by `store.Reader`;
  - capped JSON value transformation/serialization and HTTP-neutral truncation metadata.
- Refactor read methods from concrete writable `*store.Store` ownership into a `store.Reader` backed by a narrow query interface. Keep writable schema initialization, replacement, and detector persistence on `Store`; provide a reader over `Store` for existing ingest/detect code where useful.
- Migrate `internal/mcp` and `newMCPCommand` so named tools and `run_select` share the dedicated `robound` connection. Keep raw-query validation and the 1,024-row policy in `internal/mcp/sql.go`; do not make `run_select` available to dashboard code.
- Preserve existing MCP response JSON and tool behavior during extraction. Move/adjust tests before adding dashboard behavior so boundary regressions are isolated.

### Typed dashboard queries

- Add dashboard filter/view-model types and queries under `pi-session-analyzer/internal/store/` (a focused file or small group, not a generic analytics framework):
  - overview bucket series and KPI previous-period comparison;
  - paginated session matrix with stable cursor/order;
  - all-record source-ordered session page anchored by source line/ID;
  - per-session tool outcome coverage, message-token sequence, detector status, fresh findings, stale evidence, goal progression, and TODO progression.
- Generate bounded calendar bucket boundaries in Go and pass them as parameters to aggregate SQL so timezone and DST behavior are testable and independent of host/SQLite timezone.
- Avoid join fan-out when summing message cost/tokens. Prefer independent aggregates or pre-aggregated CTEs joined at one row per session/bucket.
- Derive the final stop reason and goal state with the same source-line ordering as current summaries. Keep absent, empty, and non-complete goal states distinct.
- Centralize the narrow historical error fallback currently used by detectors so dashboard inference and detector behavior call the same classifier. The classifier must be tool/context-specific rather than a broad substring match.
- Supply current detector names from `detect.Registry()` to the dashboard as protocol-neutral metadata so never-run coverage is distinguishable from successful-zero-findings and failed runs.
- Page event-stream content before serialization using the total keyset order `(source_line, kind_rank, record_id)`, where kind rank is fixed and documented for messages, calls, results, events, custom state, and custom messages. Encode all three fields in the opaque cursor and use the same ordering key for finding evidence navigation so equal-source-line records cannot be skipped or duplicated. Return previews separately from expandable bounded detail where needed so collapsed content is not eagerly shipped merely to hide it in CSS/DOM.

### HTTP server and command

- Add `pi-session-analyzer/internal/dashboard/` with embedded assets, route registration, HTTP view-model assembly, security headers, typed parameter parsing, error mapping, and graceful serving.
- Use a closed route set such as:
  - `GET /` and embedded same-origin assets;
  - `GET /api/overview`;
  - `GET /api/sessions`;
  - `GET /api/sessions/{id}` plus bounded stream/detail pagination if needed.
    Exact route decomposition may follow response-cap measurements, but it must remain typed and closed-world.
- Use `http.ServeMux` path parameters available in the repository's Go version. Apply headers consistently, reject unsupported methods, and avoid reflecting stored/request text into HTML errors.
- Add `newDashboardCommand` in `cmd/pi-session-analyzer/root.go`; flags are `--port` and `--no-open` while the existing persistent `--db` remains authoritative. Do not use `store.Open`.
- Inject listener/browser-open/server seams where needed for deterministic tests. Use context-aware `os/exec` with a finite deadline for platform browser launch; never shell-concatenate the URL.

### Frontend

- Embed a semantic HTML shell, CSS, and ES modules under `internal/dashboard/assets/`; use no external URLs, dynamic HTML strings, framework, bundler, or runtime dependency.
- Keep aggregation semantics server-side. Client modules own URL state, fetch/cancel/error behavior, SVG/table rendering, focus restoration, expansion controls, and navigation between overview, filtered matrix, and drilldown.
- Do not put collapsed transcript/TODO detail in the initial page or matrix response. Fetch bounded detail on expansion when practical; regardless, insert it only through text nodes.
- Build chart primitives around explicit data contracts and accessible companions rather than a general chart API. Use patterns/shapes/labels in addition to color for findings, detector gaps, and inferred/unknown errors.
- Put pure URL, bucketing-label, matrix-filter, and view-model transformation functions in importable ES modules and test them with Node's built-in test runner; no npm package is required. Store tests outside the embedded asset glob and add an exact `test-frontend` Makefile target (for example, `node --test internal/dashboard/frontend_test/*.test.mjs`) that `make test` invokes.

### Documentation and design reconciliation

- Update `.designs/2026-07-11-pi-session-analyzer-dashboard.md` to close its open questions and align sections 5–13 with the clarified decisions. In particular:
  - replace fixed-week default with 30-day range + adaptive day/week/month;
  - make browser-local labeled timezone explicit;
  - replace structural-only rate wording with classifiable confirmed/inferred/success/unknown semantics and coverage;
  - add TODO progression/final-list behavior;
  - make content collapsed/lazy by default;
  - replace calendar heatmap broadening with the session signal matrix;
  - remove the uPlot fallback and mark bespoke-only/no-redaction/no-Phoenix choices settled.
- Update `pi-session-analyzer/DESIGN.md` with the narrow dashboard architecture and revised non-goals, `README.md` with command usage/privacy warning, and `CLAUDE.md` with package flow and read-boundary/frontend-test invariants.
- Root docs, `go.work`, root Makefile tool list, and the relationship SVG do not change because no tool is added.

## Documentation Impact

- **`.designs/2026-07-11-pi-session-analyzer-dashboard.md`:** reconcile the accepted design with all clarified decisions; this retains the survey/rationale rather than duplicating it into user docs.
- **`pi-session-analyzer/DESIGN.md`:** authoritative behavior, architecture, trust boundary, and revised non-goals.
- **`pi-session-analyzer/README.md`:** user-facing `dashboard` command, flags, initial-ingest requirement, local URL behavior, private/not-share-safe warning, and no export/redaction claims.
- **`pi-session-analyzer/CLAUDE.md`:** dependency flow, shared bounded-read invariant, asset conventions, safe rendering rule, and focused verification commands.
- No new README or standalone guide is needed.

## Testing / Verification

- **V1 — Shared boundary (AC-2, AC-4):** table-driven tests in `internal/robound` and migrated MCP tests prove write/pragma rejection, nonexistent-DB behavior, value/timeout/row limits, valid capped JSON, truncation, cancellation, and unchanged MCP contracts.
- **V2 — Store semantics (AC-5–AC-12):** synthetic store tests cover empty and partial buckets, exact half-open boundaries, Monday weeks, DST spring/fall transitions, adaptive thresholds, invalid timestamps, split-token/no-total behavior, dedupe-safe cost, one-call outcome precedence, inferred fallback, missing/orphan/duplicate results, null rates, detector success-zero/failure-stale/never-run states, goal states, and TODO absent/malformed/clear/reopen/remove/duplicate-snapshot cases.
- **V3 — HTTP/security (AC-1–AC-4, AC-13):** `httptest` and command tests cover route/method/input rejection, loopback-only listener construction, ephemeral/explicit ports, browser-open success/failure/no-open behavior, graceful shutdown, security/no-store headers, no CORS/cookies/external assets, safe JSON errors, missing DB, read-only operation, pagination, and oversized responses.
- **V4 — Frontend units (AC-5, AC-10, AC-13):** add `pi-session-analyzer/internal/dashboard/frontend_test/*.test.mjs` and a `test-frontend` Makefile target running `node --test internal/dashboard/frontend_test/*.test.mjs`. Wire it into `make test`. Tests cover URL-state round trips, bucket/matrix filter transitions, direct single-session navigation, history-safe state, truncation/error handling, accessible label generation, and pure view-model transforms.
- **V5 — Deterministic full checks (AC-15):** from `pi-session-analyzer/`, run `go test -race ./...`, `make test-frontend`, `make build`, `make lint`, and `make audit`; from repository root, run `make build` and `make test`.
- **V6 — Browser smoke and visual inspection (AC-3, AC-6, AC-10–AC-13):** launch against a generated synthetic database with `pi-session-analyzer dashboard --db <temp-db> --no-open`, then use the Pi Playwright skill/browser harness available to the execution agent (not a committed runtime or npm dependency) to exercise the 30-day overview, timezone/bucket controls, keyboard chart selection, bucket-to-matrix-to-drilldown navigation, collapsed text, TODO/finding provenance, loading/empty/error/stale states, responsive layout, focus order, contrast/non-color cues, and zero outbound network requests. Save screenshots and browser/network evidence only under temporary paths, inspect them visually, and record the harness actions/results in completion evidence. If the execution environment lacks browser automation, this AC is blocked rather than silently downgraded to source inspection.
- **V7 — Privacy/read-only audit (AC-2, AC-3):** compare database and sidecar existence, permissions, and hashes before/after dashboard use; attempt write SQL through internal test seams; inspect browser network/storage panels for non-loopback requests, cookies, local/session storage of transcript content, and cache persistence.
- **V8 — Documentation review (AC-14):** verify design decisions, CLI help, README examples, `DESIGN.md` non-goals, and `CLAUDE.md` package flow agree; run formatting/lint checks over changed Markdown.

## Risks and Mitigations

- **Read-only enforcement is currently split and incomplete for MCP named tools.** Extract and test the boundary first; use one dedicated configured connection for both consumers rather than relying on pooled-connection pragmas.
- **Hand-written SVG can grow into a chart framework.** Keep the fixed primitive inventory and accessible HTML companions; reject zooming, animation, arbitrary series, and custom dashboards rather than vendoring a library mid-task.
- **Collapsed data can still leak if eagerly delivered or interpreted.** Fetch details lazily where practical, use no-store, never persist browser state beyond non-sensitive URL filters, and render all stored values as text.
- **Timestamp text and timezone aggregation can be wrong.** Canonicalize parseable session starts to nullable Unix instants in the writable store, index that column, compare only canonical values, compute calendar boundaries with Go/IANA data, test offsets plus 23/25-hour days and exact endpoints, and echo resolved timezone/bucket/range in responses and UI.
- **Error rates can look authoritative despite missing labels.** Classify per call, separate confirmed/inferred/unknown, expose numerator/denominator/coverage everywhere, and return null rather than zero when unclassifiable.
- **Finding staleness can disappear when no stale row exists.** Base coverage on detector registry plus detector runs; keep stale and fresh view-model types separate.
- **TODO JSON may drift or be malformed.** Parse only the known custom type and status vocabulary, treat malformed snapshots as data quality, preserve source order, and never fail the whole session view.
- **Aggregate joins can re-inflate cost or token counts.** Pre-aggregate each fact table before joining and add adversarial fan-out fixtures.
- **The 50 KB response cap can truncate normal drilldown pages.** Design preview/detail endpoints and conservative page sizes from measured synthetic worst cases; surface truncation and permit narrower paging rather than raising the cap.
- **All-history histories may exceed chart/query bounds.** Resolve to coarser calendar buckets, cap bucket count, paginate matrices, and return explicit excluded/untimed counts.
- **Auto-opening browsers is platform-dependent.** Print the URL first and treat launch failure as a warning; cover supported platform command selection without shell execution.
- **The existing design is broader and has unresolved decisions.** Reconcile it as part of the first documentation checkpoint so implementation and review use one consistent contract.

## Assumptions

- The dashboard targets current evergreen browsers with native ES modules, SVG, `fetch`, `URLSearchParams`, and accessible HTML controls.
- The current Go toolchain provides `net/http` method/path routing and embeds the IANA timezone data available on supported developer systems; tests use explicit known zones.
- Browser-local timezone is advisory input only: the browser sends an IANA name, the server validates it, and invalid/unsupported names produce a clear error with UTC as a user-selectable recovery.
- The existing `todo-state` snapshot shape and four statuses are the source contract for V1; unknown future statuses render as data-quality/unknown rather than being coerced.

## Handoff Summary

Implement this plan in the `agent-tools-research` worktree, beginning by reconciling `.designs/2026-07-11-pi-session-analyzer-dashboard.md` and extracting the shared read-only boundary before adding queries or UI. Keep every semantic rule in typed Go view models and tests, keep the frontend fixed and no-build, and verify the complete aggregate → matrix → drilldown workflow against synthetic data.

Suggested objective:

```text
/goal Implement .plans/2026-07-12-pi-session-analyzer-dashboard.md. Complete only after every acceptance criterion is satisfied with concrete test, browser, privacy, and documentation evidence.
```
