# Design: read-only visual dashboard for pi-session-analyzer

Status: proposed design (no implementation). Companion to `pi-session-analyzer/DESIGN.md`; if
accepted, this document drives a deliberate edit to that file's Non-Goals (§3.3) and a later
implementation plan.

## 1. Problem and scope

`pi-session-analyzer` already indexes Pi coding-agent sessions into a private, scrubbed SQLite
store and answers questions through a CLI and a bounded read-only MCP. What it cannot do is show
_shape_: how tool errors trend across weeks, whether compaction pressure is getting worse, how a
single session's context grew until it silently closed on an incomplete goal. Those are visual
questions, and today the only way to answer them is to hand-compose `run_select` queries and read
tables.

This design adds a **local, single-user, loopback-only, strictly view-only dashboard** with two
linked modes:

1. a **cross-session timeline** — session metrics over time (tool volume, error breakdown, token
   categories, compaction/broker-guard pressure, findings, goal outcomes, stop reasons), and
2. a **single-session drilldown** — a linear event stream with per-tool, token, finding, and
   goal-state panels.

Held boundaries (decided, not revisited here):

- Read/view-only. No writes, no config edits, no actions, no new ingest path. The dashboard is a
  read consumer of the existing store, on equal footing with the MCP surface.
- Same trust boundary as the stdio MCP: one local user, loopback only, no auth, no telemetry.
- It never joins Pi's live provider/MCP critical path and does not tail or watch files in V1.
- Data reaches the dashboard through the same `mode=ro` + `PRAGMA query_only` connection pattern
  and the same central response caps that guard the MCP. No second query path with weaker limits;
  no raw/unscrubbed tier.

## 2. Prior-art survey

Three clusters were surveyed (July 2026, primary docs where possible): LLM/agent observability
platforms, coding-agent usage analytics, and general session-analytics/APM UI patterns. Summaries
below; §2.4 distills them into named concepts with adopt/reject verdicts.

### 2.1 LLM/agent observability platforms

LangSmith, Langfuse, Arize Phoenix (+ OpenInference), Helicone, W&B Weave, OpenLLMetry/Traceloop,
and Braintrust converge on a small idiom set:

- **Span tree + waterfall/Gantt trace detail** is universal: hierarchy on the left, a selected-span
  inspector (I/O, tokens, cost, latency) on the right. Langfuse adds a tree/timeline toggle and
  inferred agent graphs; Weave adds flame-graph and call-stack scrubbers.
- **Session/conversation replay** distinct from the span tree: traces grouped by a session ID and
  rendered chat-style (Langfuse Sessions, Phoenix Sessions, LangSmith Threads).
- **Metric-over-time dashboards with group-by** (cost, tokens, request count, error rate; split by
  model/tag/user), prebuilt plus custom builders.
- **Token splits, not totals**: Langfuse models mutually exclusive usage buckets
  (`input`, `output`, `cache_read_input_tokens`, …); OpenInference has
  `llm.token_count.prompt_details.cache_read/.cache_write` and `completion_details.reasoning`;
  Braintrust shows per-span prompt/completion counts plus cache status. The mature tools already
  treat cached tokens as an exclusive bucket — exactly the store's own invariant.
- **Chart-point → filtered trace-list drilldown** is the canonical navigation glue (verified in
  LangSmith and Braintrust: click an aggregate point, land on the run list filtered to that bin).
- **Latency percentile bands** (P50/P95/P99) and error-rate time series, both presupposing volume.

Constraints that matter for this store: waterfall bar _widths_ need true per-span start/end
timestamps (the store has message timestamps of uneven reliability and no durations); latency
percentiles are meaningless at a-few-hundred-sessions scale; distributed/service spans don't exist
in a single-process agent log. Phoenix specifically is fully local-hostable (ELv2, single Docker
container, OTLP ingest, offline `TraceDataset` import) and would render faithful span order,
messages, tool calls, and token splits — but synthetic-looking waterfalls, no coding-agent-native
artifacts, and none of Pi's custom state (goal/TODO, broker guards, compaction bookkeeping,
detector findings, staleness). Self-hosting the other platforms ranges from Docker-Compose-plus-
ClickHouse (Langfuse, Helicone) to Kubernetes-only enterprise deployments (LangSmith, Weave,
Traceloop) to never-fully-local (Braintrust's hybrid control plane).

### 2.2 Coding-agent usage analytics

The Claude Code/Pi ecosystem divides cleanly:

- **Accounting is solved.** `ccusage` (with `@ccusage/pi`) owns daily/weekly/monthly/session/blocks
  token+cost reports, keeps cache-creation and cache-read columns separate from input/output, and
  has a mature three-way cost-mode design (trust logged cost / recompute / display-only). Terminal
  tables and `--json` only — no charts, no per-tool breakdown, no error analysis.
- **Quota watching is solved** (Claude Code Usage Monitor: burn rate, window progress,
  P90-of-own-history limits).
- **Transcript rendering is commoditized** (claude-code-log, cclogviewer, claude-code-trace, clog,
  simonw/claude-code-transcripts): index → project → session → collapsible tool calls, timelines,
  static HTML or localhost servers.
- **Diagnostics has exactly one under-developed incumbent**: sniffly (localhost dashboard with
  categorized error breakdowns and a user-interruption rate). Nothing in the cluster shows per-tool
  error rates, context-window pressure, compaction events, goal/task outcomes, retry/thrash
  findings, or cache efficiency as a diagnostic — the survey's explicit blind-spot list matches
  this store's differentiating columns almost one-for-one.

The cluster's shared virtues are worth copying: local files as source of truth, zero accounts,
zero telemetry, self-contained localhost or static rendering, machine-readable output alongside
the human view.

### 2.3 General session-analytics and APM UI patterns

From Amplitude/Mixpanel/PostHog and Datadog/Grafana/Honeycomb/Sentry, the durable patterns:
severity-stacked bar timelines with a fixed color ramp; top-N tables with count, share, trend
sparkline, last-seen; calendar heatmaps for activity density; small multiples over spaghetti lines
above ~5 series; big-number stat tiles with deltas; the PostHog/Amplitude single-session
**vertical event stream** (dense, expandable, filterable rows); and the three-level
**aggregate chart → filtered entity list → single entity** drill chain with all filter/time state
serialized into the URL. Equally important are the honesty rules these products encode or violate:
never stack incommensurable units, avoid dual axes (use aligned panels sharing an x-axis), render
zero buckets instead of dropping them, visually mark the partial current bucket, and when
durations are unknown, bucket by start time and _say so_ — a dot/flag timeline instead of invented
duration bars.

### 2.4 Distilled core concepts — adopt / reject

Adopted:

| Concept                                                            | Source                                         | Use here                                                                                                       |
| ------------------------------------------------------------------ | ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| **Stat-tile KPI row**                                              | Helicone, Grafana, ccusage                     | Timeline header: sessions, cost, findings, four _separate_ token tiles — never a "total tokens" tile           |
| **Severity-stacked bar timeline**                                  | Sentry/Datadog                                 | Findings per bucket with the fixed `info/warn/error` ramp; partial-bucket dimming; zeros rendered              |
| **Top-N sparkline table**                                          | Datadog top issues                             | Per-tool calls/errors ranked, trend sparkline per row, row links to filtered sessions                          |
| **Small-multiples token panels**                                   | Langfuse usage buckets + small-multiples idiom | Output / reasoning / cache-read / cache-write as four aligned panels, one x-axis, independent labeled y-axes   |
| **Session event stream**                                           | PostHog replay stream, Amplitude activity feed | The drilldown spine: source-line-ordered, expandable, filterable rows with error/compaction/guard/goal markers |
| **Chart-point → filtered list → entity drill chain, URL as state** | LangSmith, Braintrust, Grafana                 | The only navigation model; every view is a bookmarkable URL                                                    |
| **Calendar heatmap**                                               | GitHub/Grafana                                 | Compact months-scale session-density entry point (broadened view, §8)                                          |
| **Distribution strip/histogram**                                   | classic                                        | Session size distribution; at local N, show every session as a clickable point                                 |

Rejected, with reasons:

| Concept                                                                    | Why rejected                                                                                                                                                         |
| -------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Waterfall/Gantt with duration bars, flame graphs**                       | Requires per-span start/end durations the store doesn't reliably have; invented bar widths are dishonest. The drilldown uses an ordered dot/stream timeline instead. |
| **Tree/branch/agent-graph views**                                          | Pi parent chains are 100% linear in the measured corpus (0 branches); a tree toggle renders nothing and implies structure that doesn't exist.                        |
| **Latency percentile bands (P50/P99), TTFT**                               | No latency data; and local session counts make percentiles noise.                                                                                                    |
| **Error-rate _line_ at fine granularity**                                  | Small-N buckets make rates jumpy and overconfident; show error _counts_ stacked by tool, rates only in the top-N table with denominators visible.                    |
| **Cost _analytics_ (budgets, forecasts, burn gauges)**                     | Explicit non-goal ("cost analytics product"); cost stays a context series, as logged by Pi.                                                                          |
| **Correction-rate / recidivism trends**                                    | Needs branch/rewind data the corpus lacks (prior research already dropped this).                                                                                     |
| **Shareable dashboards / export cards / public links** (sniffly, viberank) | Directly contradicts the private, non-share-safe posture; export/share formats stay a non-goal.                                                                      |
| **Custom dashboard builders, natural-language chart generation**           | Framework-scale machinery for a fixed, curated view set; `run_select` via MCP already covers ad-hoc questions.                                                       |
| **Live tailing / real-time monitors**                                      | Out of scope by decision; V1 renders the index as of last ingest.                                                                                                    |

The **white space** the survey establishes: generic platforms render generic traces; the
coding-agent cluster renders accounting and transcripts. _Nobody_ renders per-tool structural
error breakdowns, compaction pressure (`tokens_before`), broker-guard policy events, goal-state
outcomes, deterministic detector findings with staleness, or an honest four-way token split — and
those are precisely the columns this store already has. That is what justifies building anything.

## 3. Buy vs build

### 3.1 The standing verdict

The pre-V1 research (`.research/agent-log-analysis/03-design-and-critique.md`, Decision 4;
`.prompts/plan-pilens-core.md`) rated browser viz "weak — most cuttable" and concluded: **export to
Phoenix/OpenInference for the generic tree/timeline/cost views; build only a narrow bespoke delta**
(failure explorer + config diff). That verdict was correct _for what it priced_: a DuckDB-WASM +
Parquet-export + four-view + live-tail app, judged before V1 existed.

### 3.2 What changed the calculus

1. **V1 closed the export path the verdict depended on.** The shipped `DESIGN.md` Non-Goals
   exclude _both_ browser visualization _and_ "OpenInference/Phoenix export and other observability
   integrations" — and the export exclusion is the better-reasoned of the two. Exporting means
   materializing a second copy of non-share-safe data (paths, usernames, prompts, code) into
   Phoenix's own storage, outside the analyzer's `0700/0600` discipline, its scrub-at-write
   boundary, and its central caps. An export artifact _is_ a share format; the same critique that
   killed "Artifact/CSP-shareable" framing kills casual Phoenix export. "Buy" is therefore not the
   cheap option anymore — it reopens a privacy decision V1 deliberately closed.
2. **What Phoenix renders well is the cheap part; what it can't render is the point.** The survey
   confirms Phoenix would show span order, messages, tool calls, and token splits — but with
   synthetic waterfalls (no durations to give it), no cross-session trend builder to speak of, and
   zero awareness of goal state, broker guards, compaction `tokens_before`, detector findings,
   staleness, or Pi's stop-reason semantics. Every view in §6–§7 that motivates this design is one
   Phoenix cannot draw from OpenInference attributes without bespoke work _anyway_.
3. **V1 already built the expensive layers.** The original L+/XL estimate priced parsing,
   normalization, storage, a query surface, and a WASM analytics bundle. Parser, scrubber, store,
   detectors, and the bounded read boundary now exist and are the hard 80%. The remaining delta is
   genuinely narrow: a handful of aggregate queries, a loopback HTTP server over embedded assets,
   and simple SVG charts.
4. **Operational footprint.** The buy path adds a permanently running third-party Python/Docker
   service to view one local SQLite file — against a repo philosophy of self-hosted, minimal-deps,
   cgo-free single binaries.

### 3.3 Decision

**Build a narrow bespoke dashboard; do not export in V1.** The dashboard is the "bespoke
failure/goal/token delta" the original verdict said to build — the difference is that with export
closed and the store built, the delta _is_ the whole deliverable, and the generic views it forgoes
(duration waterfalls, percentiles) are ones the data can't honestly support anywhere.

**Hybrid deferred, not rejected**: an explicit `export openinference` command feeding a local
Phoenix for span-tree browsing remains a coherent future step, but it must be its own design that
confronts the export/share non-goal and the second-copy privacy problem. Open question §11.2.

**Required `DESIGN.md` edit** (to land with the implementation, not before): in Non-Goals, change

> V1 excludes browser visualization, transcript timelines, export/share formats, …

to state that the exclusion of _local, loopback-only, view-only_ browser visualization and
transcript timelines is lifted, and that the following remain excluded: remote or multi-user
access, authentication, export/share formats (including screenshots-by-design affordances such as
share links or file downloads), live tailing/file watching, telemetry, identifier redaction (open
question), OpenInference export, and any write/config/action surface. A new "Dashboard" section in
`DESIGN.md` will summarize §4–§9 of this document. `README.md` gains the `dashboard` command and
restates the private/not-share-safe warning; `CLAUDE.md` gains the package-flow addition
(per Doc Purposes, the survey and rationale stay here, not in those files).

## 4. Architecture

### 4.1 Serving architecture: embedded-asset Go HTTP server (option b)

`pi-session-analyzer dashboard [--addr 127.0.0.1:0] [--db PATH]` starts a `net/http` server that
serves a small embedded frontend and a closed set of JSON endpoints, prints the bound URL, and
runs until interrupted.

- **Loopback is enforced, not defaulted.** The listener address is resolved before binding; if the
  host is not a loopback IP (`127.0.0.0/8`, `::1`), the command hard-errors. There is no flag to
  disable this.
- **Read boundary reuse.** The dashboard process opens the database exclusively through the same
  construction the MCP's `run_select` uses today: a `mode=ro` DSN with `PRAGMA query_only=ON`, the
  64 KiB SQLite value limit, and a per-query context timeout. The `executeReadOnly` helper and the
  central capped-JSON serializer currently live in `internal/mcp`; they move to a small shared
  package inside this module (e.g. `internal/robound`) so `internal/mcp` and `internal/dashboard`
  consume one implementation. This is an intra-module refactor, not a new shared module across
  tools, so it doesn't violate the copy-small-helpers convention. Every HTTP response — success or
  error — passes through the same ~50 KB capping serializer the MCP uses.
- **Closed-world endpoints only.** The dashboard exposes no raw SQL endpoint. `run_select` remains
  an MCP-only affordance; a browser-reachable SQL runner would invite cap-weakening and is exactly
  the "second query path" the invariants forbid. Aggregate queries are fixed, parameterized store
  methods (§6.1) that both surfaces could reuse later.
- **No writes anywhere.** The dashboard never calls `Store.Open` (which creates/chmods files); it
  fails with a clear message if the database doesn't exist ("run `pi-session-analyzer ingest`
  first"). WAL sidecar note: `mode=ro` on a WAL database is readable as long as the analyzer's
  normal commands maintain the sidecars; the dashboard itself never creates or repairs files.

Rejected alternatives:

- **(a) Static generator** (emit self-contained HTML/JSON snapshots): manufactures a durable,
  copyable, mailable artifact of non-share-safe data — an export format in all but name, precisely
  the posture V1 rejected. It also either embeds the entire index into every page (worse copy of
  the same problem) or gives up drilldown interactivity, and snapshots go stale invisibly. A
  server holds the data behind a socket that dies with the process; nothing lands on disk.
- **(c) Lean on `run_select`** (a generic local SQL/chart viewer over the MCP): pushes rendering
  outside the module where none of the metric-honesty rules (§5) can be encoded — any generic
  viewer will happily `SUM(totalTokens)` — and the 1,024-row/50 KB caps make multi-series trend
  assembly awkward, creating pressure to weaken exactly the limits that must not move.

### 4.2 Placement: subcommand of `pi-session-analyzer`, not a new tool

The dashboard reads this tool's private database, inherits its trust boundary, and shares its
query/cap code. A separate Go tool would need to either re-implement or share `internal/store`
scrubbed-schema knowledge — the repo convention forbids a shared internal module, and copying a
whole store layer is not a "small helper." No entry in the "Changing the Go Tool Set" checklist is
triggered; `assets/tool-relationships.svg` is unchanged.

Package flow (extends `CLAUDE.md`'s existing flow; Cobra registration stays thin):

```text
cmd/pi-session-analyzer -> internal/app, internal/mcp, internal/dashboard
internal/dashboard      -> internal/store, internal/robound
internal/mcp            -> internal/store, internal/robound
internal/store          -> internal/ingest, internal/scrub
```

`internal/dashboard` contains the HTTP handlers, loopback enforcement, embedded assets
(`//go:embed assets/*`), and the view-model assembly; aggregate SQL lives in `internal/store`
alongside the existing query methods so it is tested where every other query is tested.

### 4.3 Frontend and chart rendering: no framework, no build step, hand-rolled SVG

One embedded HTML page, one hand-written CSS file, and a small set of vanilla-JS modules that fetch
JSON and render SVG. No React/Vue, no npm, no bundler, no CDN or external font — the page must load
with `Content-Security-Policy: default-src 'self'` (plus `frame-ancestors 'none'`), which also
guarantees nothing can exfiltrate even if a crafted session smuggled active content into stored
text (all rendered strings are text-node-inserted, never `innerHTML`).

The chart inventory is deliberately simple — stacked/grouped bars, line/sparkline, dot strip,
calendar grid, and a vertical event stream — all straightforward SVG with hover tooltips and
click handlers; a charting framework earns its weight only for brushes, zooming, and animation,
none of which are in scope. If implementation shows hand-rolled charts ballooning, the fallback is
vendoring a single small dependency-free library (uPlot, ~50 KB, MIT) into the embedded assets —
flagged as open question §11.3 rather than decided against evidence we don't have yet.

Rendering is client-side from JSON endpoints (server-side SVG was considered and rejected: the
drill-chain interactivity — tooltips, click-to-filter, URL state — is the core of the design, and
doing it over server-rendered SVG means reinventing a worse frontend in Go templates). All view
state (time range, bucket size, filters, selected session, stream anchor) is serialized into URL
query parameters so every view is bookmarkable and back-button-safe.

## 5. Metric honesty — the rules every view obeys

These are design invariants, restated from the store's own semantics; a view that violates one is
wrong regardless of how it looks.

1. **The token split is never summed.** Output, reasoning, cache-read, and cache-write are kept as
   four series exactly as the store keeps them. No stacked "total tokens" chart, tile, or tooltip
   sum exists anywhere. Cache replay (cache-read) is presented as _context traffic_, visually and
   verbally separated from _generated work_ (output, reasoning). Per-message `input_tokens` exist
   in the store and appear only in the drilldown context panel, labeled as provider-reported input.
2. **Sessions are linear.** Parent/child chains have no branching in the measured corpus; the
   drilldown is a single ordered stream keyed by `source_line`, and no tree, graph, or divergence
   view exists.
3. **Cost is already deduplicated at ingest** (message primary keys; transactional session
   replacement). The dashboard aggregates `SUM(messages.cost)` per bucket and does nothing that
   could re-inflate it (no joins that fan out message rows before summing). Cost is labeled "as
   logged by Pi" and gets no forecast, budget, or projection treatment.
4. **The x-axis is session start; durations are not invented.** `sessions.timestamp` is the only
   trusted time anchor. Buckets are labeled "sessions started per day/week." No session-duration
   metric, no wall-clock waterfall, no events-per-hour rate appears anywhere. Within a session,
   order is `source_line`, not timestamps.
5. **`is_error` undercounts, and the dashboard says so.** Structural error counts
   (`tool_results.is_error = 1`, with the same name-fallback join the summary uses) are labeled
   "structural errors." Historical MCP failures that Pi never flagged are _not_ re-derived in
   dashboard SQL; they surface through the `mcp-failure` detector's findings (which already encode
   the labeled text fallback and its evidence source) and render as findings, never merged into the
   structural counts. Error panels carry a persistent footnote: "structural `is_error` only;
   historical MCP failures appear as detector findings."
6. **Stale is never current.** Findings from a detector whose latest run failed render only with a
   prominent stale badge and the run's error summary, and are excluded from all aggregate finding
   counts (§9). Heuristic findings are always labeled `heuristic` and never presented as
   structural facts — classification is a visible field and filter, not a footnote.
7. **Small-N discipline.** Rare signals (compactions, broker guards, MCP failures were 16/42/9 in
   the measured corpus) are shown as discrete counts/marks, not smoothed rates or trend claims.
   Partial current buckets are dimmed; empty buckets render as zeros.

## 6. Cross-session timeline view

### 6.1 Time model and query shape

- x-axis: `sessions.timestamp` (session start), bucketed by day / week / month; user-selectable,
  default **week** (open question §11.4). Bucketing happens in SQL
  (`strftime` over the ISO timestamp) via new parameterized store methods, e.g.
  `TimelineSeries(ctx, bucket, from, to, cwdFilter)`.
- Every aggregate endpoint returns at most a few hundred bucket rows (a bounded date range and a
  fixed series list keep results far under the caps); responses still pass through the central
  capped serializer, and queries run under the standard timeout on the read-only connection.
- A global filter bar (time range, project/cwd, and where applicable tool, detector, severity,
  classification) applies to every panel; filters are URL-encoded chips.

### 6.2 Panels (each names its exact source)

1. **KPI tiles**: sessions started; total cost; findings by severity (fresh only); and four
   separate token tiles (output / reasoning / cache-read / cache-write), each with a delta vs the
   previous equal-length period and a sparkline. Sources: `sessions`, `messages`, `findings` ⋈
   `detector_runs`.
2. **Session volume + size distribution**: sessions-started bars; beside it, a dot-strip/histogram
   of `sessions.total_records` (every dot a clickable session). Message/turn counts per session
   come from a `COUNT(messages)` group.
3. **Tool-call volume**: stacked bars per bucket by `tool_calls.name`, top 8 tools + "other"
   (categorical palette, never the severity ramp).
4. **Tool errors**: stacked bars of structural error _counts_ per bucket by tool
   (`tool_results.is_error=1`, name fallback join as in `SessionSummary`); the per-tool **top-N
   table** beside it shows calls, errors, error share with visible denominators, and a per-row
   sparkline. Carries the §5.5 footnote.
5. **Token categories**: four small-multiple panels sharing the x-axis — output, reasoning,
   cache-read, cache-write per bucket — independent, labeled y-axes; grouped visually as
   "generated" (output, reasoning) vs "context traffic" (cache-read, cache-write). No stack, no
   total, no dual axis.
6. **Cost**: bars per bucket of `SUM(messages.cost)`, labeled "as logged by Pi."
7. **Pressure events**: compactions per bucket (`events.type='compaction'`), with max
   `tokens_before` in the tooltip; broker guards per bucket (`custom_messages.type='broker-guard'`),
   segmented by `kind`. Rendered as discrete count bars/marks (§5.7).
8. **Findings over time**: severity-stacked bars (fixed info→warn→error ramp), fresh findings
   only, filterable by detector and classification; heuristic segments carry the label in tooltip
   and legend. A staleness strip above the chart marks buckets containing sessions with
   failed/stale detectors (§9).
9. **Goal outcomes**: per-bucket mix of terminal goal state — `complete` / other terminal status /
   started-status-unavailable / no goal started — from the last `custom_state.type='goal-state'`
   row per session (same query semantics as `SessionSummary`).
10. **Stop-reason mix**: per-bucket bars of each session's final assistant `stop_reason` (same
    "last non-empty by source line" rule the summary uses).

### 6.3 Linking into the drilldown

Every bar segment, dot, table row, and heatmap cell is a filter mutation: clicking appends its
bucket/category to the URL filter state and routes to the **session list** — a sortable table
(start time, cwd, records, cost, findings by severity, goal state, stop reason, schema drift)
scoped to those filters, reusing the bounded list semantics of `ListSessions` with paging. Clicking
a row opens the drilldown at `?session=<id>`. Aggregate → list → entity, with the filter chips
visible and individually removable at every step.

## 7. Single-session drilldown

Layout: header, then a two-column body — the event stream as the spine, panels beside it. All
panels derive from queries the store already answers (`SessionSummary`, `Conversation`,
`LoadSession`-shaped per-table reads) exposed as bounded endpoints.

1. **Header tiles**: start timestamp, cwd, source path, total/malformed/unknown records, schema
   drift, cost, the four token tiles (separate, as always), final stop reason, terminal goal
   state.
2. **Detector freshness banner**: if any `detector_runs.status='failed'` or any stale finding
   exists, a banner names the detectors, their `error_summary`, and generation — before any
   findings render (§9).
3. **Linear event stream** (the spine): all messages, tool calls, tool results, events, and custom
   rows interleaved by `source_line` — the PostHog/Amplitude stream idiom over the existing
   `Conversation` union extended with `events`/`custom_state`/`custom_messages` rows. Each row:
   type icon, role/tool name, one-line scrubbed text preview, expandable detail. Error results get
   the severity ramp; compactions render as full-width markers showing `tokens_before`; broker
   guards as policy markers with `kind`; goal-state changes as status transitions. Type-filter
   chips (messages / tools / errors / events). Paged by anchor + limit exactly like
   `get_conversation` (100 per page), with the page window in the URL. The x-dimension is sequence
   — no wall-clock spacing.
4. **Per-tool table**: calls, structural errors, error share for this session, with the §5.5
   footnote; rows filter the stream to that tool.
5. **Token-by-message panel**: two vertically aligned mini-panels sharing the sequence axis —
   "generated" (output + reasoning bars per assistant message) and "context" (input, cache-read,
   cache-write per assistant message) — with compaction markers as vertical rules carrying
   `tokens_before`. This shows context growth and the compaction sawtooth honestly, from
   per-message provider-reported numbers, without claiming a continuous context-size series.
6. **Findings panel**: grouped by detector; each finding shows severity chip, classification
   label, summary, evidence ID, and source line; clicking scrolls/pages the stream to that line
   (provenance is the point). Stale findings appear only under a "stale — last run failed" badge
   with the run error, visually inert (greyed, excluded from the header severity tiles).
7. **Goal progression strip**: the sequence of `goal-state` statuses by source line, ending at the
   terminal state shown in the header.

## 8. Broadened visualizations (each justified against existing columns)

In scope — data already exists:

- **Project facet**: `sessions.cwd` powers a per-project breakdown table and a cwd filter chip on
  every timeline panel (`list-sessions --cwd` already establishes the filter semantics).
- **Model mix**: `messages.model` per bucket — a session's model changes are real Pi events and
  the column is populated; rendered as a categorical stacked bar.
- **Data-quality panel**: `malformed_records`, `unknown_records`, `schema_drift` per bucket — an
  early-warning view for Pi schema evolution that nothing else in the ecosystem shows.
- **Calendar heatmap**: sessions-started density (weeks × weekday grid, quantized scale), as a
  compact long-range entry point; each cell click-filters the session list.
- **Detector-freshness board**: all `detector_runs` with `status='failed'` across sessions, with
  error summaries — the operational "is my index trustworthy" view (§9).
- **Top-failures board**: the `TopFailures` query rendered as a filterable table (severity,
  detector, classification), linking into drilldowns — the MCP tool's visual twin.

Explicitly out, because the data can't support them: anything duration/latency-based; percentile
panels; branch/tree views; correction-rate metrics; cache _hit-ratio efficiency_ claims (the store
has token counts, not request-level hit accounting); acceptance-rate metrics (no accept/reject
signal); config-cohort comparisons (no config fingerprint — still blocked upstream); budget/burn
gauges (cost-analytics non-goal).

## 9. Freshness and staleness are first-class

Two distinct kinds, both always visible:

- **Detector staleness** (modeled in the store): stale findings and failed `detector_runs` are
  excluded from every aggregate count and severity tile; they render only in dedicated stale
  sections with the failed run's error summary and generation. The timeline gets a staleness strip
  (§6.2.8); the drilldown gets the banner (§7.2); the freshness board (§8) gives the global view.
  The dashboard must be incapable of rendering a stale finding as current — enforced by the
  view-model layer separating `fresh` and `stale` collections rather than passing a flag to the
  frontend to honor.
- **Index staleness**: every page footer shows "index as of <MAX(sessions.ingested_at)>" plus the
  ingest hint (`pi-session-analyzer ingest`). V1 does not watch or stat the source directory (no
  tailing, no critical-path adjacency); the dashboard renders the index, and says which index.

## 10. Privacy posture

The database intentionally retains identifiers (paths, usernames, hosts, URLs, prompts, responses,
code) after credential-value scrubbing; a dashboard makes that content trivially screenshottable.
Posture, stated plainly:

- Loopback-only bind, hard error otherwise; no auth (one local user is the trust boundary, same as
  the stdio MCP); no TLS (loopback).
- **No share, export, download, copy-permalink-to-cloud, or print-optimized affordances of any
  kind.** URLs are local bookmarks, nothing more. The page header carries the same warning the
  README does: _private — not safe to share or screenshot_.
- No telemetry, no external requests (CSP `default-src 'self'`), no cookies, `Cache-Control:
no-store` on every response so session content doesn't persist in browser caches.
- Data on the wire is the same scrubbed, capped data the MCP serves — the dashboard adds a
  renderer, not a new disclosure tier; nothing new lands on disk.
- An optional identifier-redaction / "present-safe" mode (blur paths/usernames/hosts for
  screen-sharing) is **not in V1** — it would be a new scrub tier with its own failure modes, and
  V1's non-goal explicitly excludes identifier redaction. Open question §11.1.

## 11. Open questions (for the user — deliberately not resolved here)

1. **Present-safe mode**: is a best-effort identifier-blur toggle (CSS/DOM-level, clearly labeled
   as cosmetic, not a security boundary) worth having for screen-sharing, or does its existence
   invite false confidence? V1 ships without it.
2. **Phoenix-export hybrid**: should a follow-up design revisit `export openinference` (feeding a
   local Phoenix for span-tree browsing), which requires deliberately reversing the export
   non-goal and answering the second-copy privacy problem? Nothing in this design depends on it.
3. **Chart rendering fallback**: hand-rolled SVG is the plan; if implementation shows it
   ballooning, vendor uPlot (~50 KB, MIT, dependency-free) into the embedded assets. Acceptable?
4. **Default time bucket**: week (smoother at ~150-session scale) vs day (matches ccusage habit).
   Design assumes week; both are one click away regardless.
5. **MCP parity for aggregates**: the new timeline aggregate queries could later back an MCP tool
   (e.g. `metrics_timeline`) so agents get the same trends the dashboard shows. In scope for a
   later increment, not V1?

## 12. Testing and verification sketch

- Store aggregate queries: table-driven tests over synthetic fixtures (the existing fixture
  discipline — never real sessions), including bucket-boundary, empty-bucket, dedupe-preserving,
  and stale-exclusion cases.
- HTTP layer: `httptest` coverage for loopback enforcement (non-loopback addr errors), CSP/no-store
  headers on every route, cap enforcement (oversized synthetic payload truncates to valid JSON),
  read-only behavior (endpoints function against a read-only database file; no file is created or
  chmodded by the dashboard command), and 404/error shapes.
- Frontend: keep logic thin enough that Go-side view-model tests carry the semantics (fresh/stale
  separation, token categories never summed); a small set of golden JSON view-model fixtures.
- Manual verification per repo convention: run against a real local corpus, confirm no network
  requests leave loopback (browser devtools), and confirm the stale/fresh rendering against a
  forced detector failure.

## 13. Summary of decisions

| #   | Decision                                                                                                                                                                                                                           |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Build a narrow bespoke dashboard; no Phoenix/OpenInference export in V1 (hybrid deferred). `DESIGN.md` Non-Goals edited accordingly, with the implementation.                                                                      |
| 2   | Loopback-only embedded-asset Go HTTP server behind a `dashboard` subcommand; static-generator and run_select-viewer options rejected (export-artifact and weak-boundary risks).                                                    |
| 3   | Subcommand + `internal/dashboard` package in the existing module; read path shared with MCP via an intra-module `internal/robound` refactor; no new Go tool.                                                                       |
| 4   | Client-rendered hand-rolled SVG, vanilla JS, no build step, strict same-origin CSP; uPlot vendoring as fallback.                                                                                                                   |
| 5   | Two linked modes — bucketed timeline (x = session start) and linear source-line drilldown — joined by URL-state filter chips.                                                                                                      |
| 6   | Metric honesty invariants (§5): four-way token split never summed, linear streams only, dedupe preserved, no invented durations, structural-error labeling with detector-finding overlay, stale-never-current, small-N discipline. |
| 7   | Privacy: loopback hard-fail, zero share/export affordances, zero telemetry, no-store, self-contained assets; redaction mode explicitly deferred.                                                                                   |
