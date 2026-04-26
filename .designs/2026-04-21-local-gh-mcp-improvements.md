# local-gh-mcp Improvements — Design

Date: 2026-04-21

## Context

Following the review in `local-gh-mcp/REVIEW.md` against the MCP 2025-06-18 spec, Anthropic's "Writing tools for agents" guidance, and the official `github/github-mcp-server`, this document records the scope decisions for a focused polish pass on `local-gh-mcp`.

Breaking changes are acceptable. The only consumer is `mcp-broker`, which we own; sandboxed-agent conversations may hold stale tool schemas but will refresh on reconnect.

## Goals

1. Give agents accurate tool metadata (annotations, enums, required/default fields in the schema) so they fail at validation, not runtime.
2. Eliminate the most common agent-misfire patterns (`number` ambiguity, `gh_check_pr` naming, list/search overlap).
3. Close the biggest capability gaps (`gh_whoami`, `gh_view_file`, `gh_list_pr_files`, per-job run logs, branch/release listing, PR state transitions).
4. Cap unbounded outputs (`gh_diff_pr`, run logs) so the context window can't be blown by a single call.

## Scope Summary

Included: 11 work items across annotations, naming, schema, descriptions, new tools, parameter polish, and error messages. (14 originally; 3 dropped — 2 during Phase 3 review, 1 during Phase 4 review — see Skipped.)

Explicitly skipped: server-wide `--read-only`, `structuredContent`/`outputSchema`, medium-value new tools, body sanitization. Reasons recorded in the "Skipped" section.

Tool count delta: 28 → 37 (9 new tools, 2 renamed, 1 param renamed across 17 tools).

## Included Items

### 1. MCP tool annotations

Set `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint` on every tool in the `mcp.NewTool(...)` call sites. Today none are set, so MCP clients treat every tool as potentially destructive.

Classification (from `REVIEW.md` section 2):

- **Read tools** (all `gh_view_*`, `gh_list_*`, `gh_diff_pr`, `gh_check_pr`/`gh_list_pr_checks`, `gh_search_*`, plus new `gh_whoami`, `gh_list_pr_files`, `gh_view_file`, `gh_list_branches`, `gh_view_run_job_logs`, `gh_list_releases`, `gh_view_release`) → `readOnlyHint: true`.
- **Additive writes** (`gh_create_pr`, `gh_comment_pr`, `gh_comment_issue`, `gh_review_pr`) → `destructiveHint: false` explicitly.
- **Idempotent writes** (`gh_edit_pr`, `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`) → `idempotentHint: true`, `destructiveHint: false`.
- **Destructive writes** (`gh_merge_pr`, `gh_close_pr`, `gh_cancel_run`, `gh_delete_cache`) → `destructiveHint: true`.
- **Non-destructive re-execution** (`gh_rerun_run`) → `destructiveHint: false`.
- **All tools** → `openWorldHint: true` (they all touch GitHub).

### 2. Parameter rename — `number` → `pr_number` / `issue_number`

Rename the `number` parameter everywhere it appears:

- 13 PR tools: `gh_view_pr`, `gh_diff_pr`, `gh_comment_pr`, `gh_review_pr`, `gh_merge_pr`, `gh_edit_pr`, `gh_list_pr_checks`, `gh_close_pr`, `gh_list_pr_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments`, `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr` → `pr_number`
- 4 issue tools: `gh_view_issue`, `gh_comment_issue`, `gh_list_issue_comments` → `issue_number`

Agents juggling PR and issue tools in the same turn currently misfire on `gh_view_issue({pr_number: 42})` and the inverse. Resource-prefixed names remove the ambiguity.

### 2b. Tool renames

- `gh_check_pr` → `gh_list_pr_checks` — "check" as a verb is ambiguous; the tool lists CI check runs as markdown bullets, matching every other `gh_list_*` tool.
- `gh_rerun` → `gh_rerun_run` — every other workflow-run tool is `gh_<verb>_run` (`gh_list_runs`, `gh_view_run`, `gh_cancel_run`). `gh_rerun_run` reads awkwardly but keeps the pattern.

### 3. Enums in JSON Schema

Declare `"enum": [...]` for parameters with fixed value sets. Today valid values live only in description strings and handler validation, so invalid values fail server-side rather than at schema validation.

| Tool                                 | Param    | Enum                                              |
| ------------------------------------ | -------- | ------------------------------------------------- |
| `gh_list_prs`, `gh_search_prs`       | `state`  | `open`, `closed`, `merged`, `all`                 |
| `gh_list_issues`, `gh_search_issues` | `state`  | `open`, `closed`, `all`                           |
| `gh_review_pr`                       | `event`  | `approve`, `request_changes`, `comment`           |
| `gh_merge_pr`                        | `method` | `merge`, `squash`, `rebase`                       |
| `gh_list_runs`                       | `status` | (set from `gh run list --status`)                 |
| `gh_list_caches`                     | `sort`   | `created_at`, `last_accessed_at`, `size_in_bytes` |
| `gh_list_caches`                     | `order`  | `asc`, `desc`                                     |

Normalizes the state-enum drift flagged in the review (TL;DR #4) as a side effect: PRs and their search tool share the same set; issues and their search tool share the same set; `all` is present wherever it applies.

Keep handler validation as defense-in-depth. The MCP library's schema validation is client-dependent; the handler stays authoritative.

### 5. Sharpened tool descriptions

Rewrite one-line tool descriptions in the style of the existing `gh_list_pr_comments` description (which explicitly disambiguates itself from `gh_list_pr_reviews` and `gh_list_pr_review_comments`). Target 2–3 sentences per tool — terse but disambiguating, not paragraphs.

Priority targets (rest are mechanical):

- `gh_list_prs` vs `gh_search_prs` — list when you have owner/repo; search when you need cross-repo or the GitHub search DSL.
- `gh_list_issues` vs `gh_search_issues` — same distinction.
- `gh_search_*` `query` param — mention the GitHub search DSL explicitly and give one worked example, e.g. `is:open author:@me review-requested:@me`.
- `gh_view_run` — document the JSON-vs-raw-logs switch triggered by `log_failed`.
- `gh_edit_pr` — list the fields it does _not_ change (state, draft-readiness) and point at `gh_ready_pr` / `gh_draft_pr` / `gh_reopen_pr` / `gh_close_pr`.
- `gh_search_*` — add "Results truncated at 100; refine your query if you need more." (10e).

### 6. New tool — `gh_whoami`

No-argument tool returning the authenticated GitHub user. Shells to `gh api /user` (or `gh auth status` if we want token scopes).

Lives in a new `internal/tools/context.go` — new "context" category, matching the official server's `context` toolset. Room for future identity-related tools (notifications, assigned PRs, etc.) without polluting the PR/issue categories.

Unlocks `author:@me`-style queries by grounding the agent in "who am I via this server." Annotations: `readOnlyHint: true`, `openWorldHint: true`.

### 9. PR state transition tools — separate per action

Three new tools, parallel to the existing `gh_close_pr`:

- `gh_ready_pr` — `gh pr ready <pr_number>`. Marks a draft PR ready for review.
- `gh_draft_pr` — `gh pr ready <pr_number> --undo`. Converts a ready PR to draft.
- `gh_reopen_pr` — `gh pr reopen <pr_number>`. Reopens a closed PR.

Naming: `gh_draft_pr` over `gh_unready_pr` — matches GitHub's own vocabulary ("Convert to draft"), findable by intent, state-symmetric with `gh_ready_pr`.

Why separate tools instead of folding into `gh_edit_pr` (as the review originally suggested): each maps 1:1 to a `gh` subcommand, handlers stay clean, annotations are per-transition, and it's symmetric with the existing `gh_close_pr`. Modest tool-count cost is worth the clarity.

All three: `idempotentHint: true`, `destructiveHint: false` (state transitions, not destructive edits).

### 10. Parameter design polish

**10a. Cap unbounded outputs.**

- `gh_diff_pr`: add `max_bytes` (default 50000, max 500000). Truncate on a line boundary with `[truncated — N/M bytes shown]` trailer. Don't add `max_files` — `max_bytes` is more predictable.
- `gh_view_run` with `log_failed=true`: add `tail_lines` (default 500, max 5000). Matches the official server's pattern.

**10b. Declare `default` fields in schema.**

Add `"default": N` to `limit` (30) and `max_body_length` (2000) schema entries. LLMs often ignore `default`, but schema-validating clients display them; cheap to declare and consistent with the enum work.

**10c. Remove `search` param from list tools.**

Drop `search` from `gh_list_prs` and `gh_list_issues` — today both accept a `gh pr list --search <query>` passthrough that uses the same GitHub search DSL as `gh_search_prs` / `gh_search_issues`. Two overlapping entry points confuses agents. Redirect to the dedicated search tools in the list tools' descriptions (see #5).

**10e. Clarify 100-result cap in search descriptions.**

Folded into #5. One sentence per `gh_search_*` tool: "Results truncated at 100; refine your query if you need more."

**10f. New filters on `gh_list_runs`.**

Add `actor` (who triggered the run) and `event` (push, pull_request, schedule, workflow_dispatch, etc.) — both pass through to `gh run list --user` / `--event`. Skip `created` — its own DSL (`>=YYYY-MM-DD`) is awkward without broader search-DSL context.

### 11. New tools

Six high-value additions flagged in the review.

**11a. `gh_list_pr_files`** — files a PR touches with `+`/`-` counts per file, no diff content. Maps to `gh pr view <num> --json files` or `gh api repos/O/R/pulls/N/files`. Answers "which files does this PR touch?" without the full-diff cost.

**11c. `gh_list_branches`** — list branches in a repo. Maps to `gh api repos/O/R/branches`. Small utility; closes the gap for agents composing branch-based PRs.

**11d. `gh_view_run_job_logs`** — per-job logs with `job_id` and `tail_lines`. Complementary to `gh_view_run` with `log_failed=true`: that returns concatenated failed-job logs for the whole run, which on a multi-job failure is unusably large. Per-job is the targeted version. Needs `gh.ListRunJobs()` + `gh.ViewRunJobLog()` in the client layer.

**11e. `gh_list_releases` + `gh_view_release`** — release metadata. `gh release list` / `gh release view`. Two tools. Useful for release notes generation and versioned-bug triage.

### 12. Error message polish

**12a. Terser parse-failure errors.**

Replace:

```go
return gomcp.NewToolResultError(fmt.Sprintf("failed to parse PR JSON: %v", err)), nil
```

With: log the raw `gh` stdout at `slog.Error` (server-side), return to caller:

```
"internal error: unable to parse gh output; check server logs"
```

The agent cannot fix a server-side JSON parsing mismatch; surfacing the raw parse error encourages fruitless retries.

**12b. Batched missing-required-field errors.**

Aggregate missing fields in a loop and report all at once: `"gh_create_pr: required fields missing: title, body, head"`. Saves round-trips when an agent forgets multiple fields.

During implementation, check whether the MCP library's required-field validation (from the JSON Schema `required` array) does this for free. If it does, we just remove the handler checks. If not, do it manually.

### 14. Section-11 behavior tweaks

**14a. `gh_create_pr` — accept empty `body`.**

Today the handler rejects empty bodies. Some repos rely on PR templates to auto-fill — the agent should be allowed to pass an empty body and let GitHub apply the template. Drop the empty-body rejection in the handler; let GitHub decide.

## Skipped

**#7 Server-wide `--read-only` flag.** `mcp-broker` already enforces read-only posture at the proxy layer. One enforcement point is better than two, and the annotations from #1 give mcp-broker the signal it needs for per-tool filtering.

**#8 `structuredContent` + `outputSchema`.** No current consumer needs parseable JSON alongside the markdown. Every `outputSchema` becomes a versioned contract with maintenance cost; the markdown-for-LLM story works today. Revisit when an automation use case emerges — per-tool, on demand.

**#10d `sort` / `order` on `gh_list_prs` and `gh_list_issues`.** Dropped during Phase 3 review. `gh` doesn't expose these flags natively; implementation would require constructing `--search` DSL fragments internally (conflicts with #10c's "no overlap" principle) or switching to `gh api` (new plumbing for a niche feature). `gh_search_prs` / `gh_search_issues` already serve sorted queries via the DSL (`sort:created-asc`).

**#11 medium-value tools.** `gh_list_commits` / `gh_view_commit`, `gh_update_pr_branch`, `gh_list_workflows` / `gh_view_workflow`, `gh_list_notifications` / `gh_mark_notification_read`, `gh_list_code_scanning_alerts`. Each is useful in specific scenarios but none are universal. Keep the tool-count growth bounded (38 is already a jump from 28).

**#11b `gh_view_file`.** Dropped during Phase 3 review. Sandboxed agents already have the repo checked out; file reads are `cat <path>` or `git show <ref>:<path>`. Hidden edge cases (binary detection, LFS pointers, encoding, symlinks) don't pay for themselves. Revisit if a concrete cross-repo use case emerges.

**#14b `gh_list_issue_comments` `since` filter.** Dropped during Phase 4 review. The stated motivation ("incremental triage — only fetch comments added after last check") assumes the caller persists "last check" between calls, but an MCP server has no such state and agents don't reliably track timestamps across turns. The existing `limit` already gives callers a knob to bound responses. Revisit if a concrete agent workflow emerges that tracks per-call cursors.

**#13 Body sanitization.** The prompt-injection threat from hidden Unicode in GitHub content is real but secondary — mcp-broker's auto-approval gates destructive actions at a layer above this. Revisit if a concrete incident shows sanitization would have changed the outcome.

## Tool Inventory

Before: 28 tools. After: 37 tools.

**New (9):** `gh_whoami`, `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`, `gh_list_pr_files`, `gh_list_branches`, `gh_view_run_job_logs`, `gh_list_releases`, `gh_view_release`.

**Renamed (2):** `gh_check_pr` → `gh_list_pr_checks`; `gh_rerun` → `gh_rerun_run`.

**Param renamed (17 tools):** `number` → `pr_number` on 13 PR tools (including `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr` from this doc); `number` → `issue_number` on 4 issue tools.

**New category:** `internal/tools/context.go` for `gh_whoami` and any future identity/context tools.

## Implementation Ordering

Suggested sequence. Each phase is independently shippable, but earlier phases de-risk later ones.

**Phase 1 — Schema polish (non-breaking).**

1. Annotations on all tools (#1).
2. Enums in schema + `default` fields (#3, #10b).
3. Sharpened descriptions (#5, #10e).
4. Error message polish (#12).

No caller breakage. Land first to get the polish out and to exercise the test suite with the new schemas.

**Phase 2 — Renames (breaking).**

5. Rename `number` → `pr_number` / `issue_number` (#2).
6. Rename `gh_check_pr` → `gh_list_pr_checks`; `gh_rerun` → `gh_rerun_run` (#2b).

One coherent breaking release. Regenerate any memory/notes the agent ecosystem has with old names; announce via whatever channel mcp-broker uses for version bumps.

**Phase 3 — New tools and filters (additive).**

7. `gh_whoami` + new `context` category (#6).
8. State-transition tools: `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr` (#9).
9. New filters: `gh_list_runs` `actor`/`event` (#10f).
10. Remove `search` from list tools (#10c) — technically breaking, bundle with these adds.
11. `gh_list_pr_files`, `gh_list_branches` (#11a, #11c).
12. `gh_view_run_job_logs` (#11d).
13. `gh_list_releases`, `gh_view_release` (#11e).

**Phase 4 — Behavior changes and output caps.**

14. Caps on `gh_diff_pr` / `gh_view_run` (#10a).
15. `gh_create_pr` accept empty body (#14a).
16. ~~`gh_list_issue_comments` `since` filter (#14b)~~ — dropped, see Skipped.

## Non-Goals

- Server-wide toolset grouping (`--toolsets`, `--dynamic-toolsets`). Not justified at 38 tools.
- Pagination on list/search tools. Intentional per `DESIGN.md`; agents should refine queries, not scroll.
- Raw `gh api` escape hatch. Intentional per `DESIGN.md`; schema validation is a core feature.
- Response-format toggle (`response_format: "concise" | "detailed"`). The current output level is the intended default; adding a "more detail" mode is speculative until we see agents asking for it.

## Follow-ups Out of Scope

Items noted during the review but not addressed here:

- Consolidation to `_read`/`_write` tools with a `method` enum (like the official server). Revisit only if tool count grows past ~45.
- Per-tool output-shape documentation — currently lives in `DESIGN.md`'s "Output Format" section; each new tool added in Phase 3 needs its entry there at implementation time.
