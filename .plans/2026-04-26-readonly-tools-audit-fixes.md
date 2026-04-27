# local-gh-mcp Read-Only Tools Audit Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Resolve all 28 findings from `local-gh-mcp/readonly-tools-audit.md`, fixing leaked-error UX, format inconsistencies, and schema/description mismatches across the 24 read-only MCP tools.

**Architecture:** Each task is one coherent change landing as a single commit. Tasks are ordered so high-severity error-output bugs land first, mechanical list-tool standardization happens together, and search-tool work clusters at the end. Findings #15 and #26 are resolved transitively (by #7 and #11 respectively) so they don't get standalone tasks. The audit findings file is removed in the final task.

**Tech Stack:** Go, mcp-go v0.45.0, gh CLI, MCP stdio.

---

### Task 1: Error-output hygiene for `gh api` and `gh search` callsites

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (`cleanAPIError` at lines 110-124; `PRReviewComments` at line 507; `SearchPRs`/`SearchIssues`/`SearchRepos`/`SearchCode`/`SearchCommits` error returns)
- Test: `local-gh-mcp/internal/gh/gh_test.go`

**Acceptance Criteria:**

- `cleanAPIError` extracts the `gh: …` line even when `gh` concatenates JSON body and error line without a newline (use `strings.LastIndex(s, "gh: ")`, then trim at first newline after match). Falls back to trimmed input if `gh: ` not found.
- `PRReviewComments` failure path routes through `cleanAPIError`, not `strings.TrimSpace`.
- New `cleanGhError(out []byte) string` helper in `gh.go` returns the first `Error: ` line if present, otherwise the first non-empty line, otherwise the trimmed input. All five `Search*` wrappers use it on failure.
- New tests cover: (a) JSON-body-then-`gh:`-line concatenation case for `cleanAPIError`; (b) `Error: <msg>` followed by usage text for `cleanGhError`.

**Notes:** Resolves findings #1 and #4. Both helpers are pure — test by passing crafted byte slices, no `gh` invocation needed. CLAUDE.md "parse errors" rule applies to JSON parsing; these helpers handle the separate concern of `gh` runtime errors but follow the same "never leak raw output" principle.

@test-driven-development

**Commit:** `fix(local-gh-mcp): tighten gh api/search error rendering`

---

### Task 2: Search-tool state translation (drop unsupported gh flags)

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (`SearchPRs` at lines 672-700; `SearchIssues` at lines 703-720)
- Modify: `local-gh-mcp/internal/tools/search.go` (`handleSearchPRs` lines 208-242; `handleSearchIssues` lines 244-278)
- Test: `local-gh-mcp/internal/tools/search_test.go`, `local-gh-mcp/internal/gh/gh_test.go`

**Acceptance Criteria:**

- `gh_search_prs` with `state=merged` invokes `gh search prs` without `--state`; the token `is:merged` is appended to the shlex-split query tokens before forwarding.
- `gh_search_prs` with `state=all` and `gh_search_issues` with `state=all` both invoke `gh search …` without `--state`.
- `gh_search_prs` schema enum stays `["open","closed","merged","all"]`; `gh_search_issues` schema enum stays `["open","closed","all"]`.
- New tests verify the handler builds the correct argv: `state=merged` passes no `--state` and includes `is:merged` token; `state=all` passes no `--state`; `state=open` and `state=closed` still pass `--state`.

**Notes:** Resolves findings #2 and #3. Translation lives at the wrapper boundary — pass a translated `SearchPRsOpts`/`SearchIssuesOpts` and re-shape the `query` token list. Order with Task 9 (search filter conflict rejection): conflict detection runs first, then `is:merged` injection, so a user passing both `state=merged` and `is:` in `query` correctly errors.

@test-driven-development

**Commit:** `fix(local-gh-mcp): translate search state for unsupported gh values`

---

### Task 3: Run-log normalization shared helper

**Files:**

- Modify: `local-gh-mcp/internal/gh/gh.go` (extract `normalizeRunLog`; apply in `ViewRun` lines 549-563 and `ViewRunJobLog` lines 572-584)
- Test: `local-gh-mcp/internal/gh/gh_test.go`

**Acceptance Criteria:**

- New unexported `normalizeRunLog(out string) string` strips a leading UTF-8 BOM (`﻿`) and replaces `\tUNKNOWN STEP\t` with `\t`.
- `ViewRun` with `logFailed=true` returns normalized output (handler's `tailLines` + `TruncateBytes` then operate on clean lines).
- `ViewRunJobLog` calls `normalizeRunLog` instead of inlining BOM strip and `UNKNOWN STEP` replace.
- Tests cover: BOM strip, `UNKNOWN STEP` collapse, both applied to each path.

**Notes:** Resolves finding #6. Tail logic stays where it is in each path: handler-side for `ViewRun`, gh.go-side for `ViewRunJobLog`. Don't refactor that here.

@test-driven-development

**Commit:** `refactor(local-gh-mcp): share run-log normalization across paths`

---

### Task 4: Author bot-suffix flattening

**Files:**

- Modify: `local-gh-mcp/internal/format/format.go` (`FormatAuthor` lines 22-32)
- Modify: `local-gh-mcp/internal/format/github.go` (`FormatRelease` lines 662-664)
- Test: `local-gh-mcp/internal/format/format_test.go`, `local-gh-mcp/internal/format/github_test.go`

**Acceptance Criteria:**

- `FormatAuthor` strips a literal `[bot]` suffix from `login` when present and forces `IsBot=true` semantics, regardless of struct's `IsBot` field. So `Author{Login: "github-actions[bot]", IsBot: false}` renders as `@github-actions [bot]`.
- `FormatRelease` calls `FormatAuthor(r.Author)` instead of `fmt.Fprintf(&b, "by @%s", r.Author.Login)`. Output line shape becomes `by @login [bot]` for bot authors, `by @login` otherwise.
- Tests cover: `FormatAuthor` with `[bot]` suffix and `IsBot=false` (defensive path); `FormatRelease` with bot author renders `by @<name> [bot]`.

**Notes:** Resolves finding #5. The defensive `[bot]` strip helps any other endpoint where `is_bot` isn't populated reliably.

@test-driven-development

**Commit:** `fix(local-gh-mcp): flatten bot author logins in releases`

---

### Task 5: List-tool standardization (empty cases, limit+1 detection, unified marker)

**Files:**

- Modify: `local-gh-mcp/internal/format/format.go` (`TruncateBody` lines 50-66; `TruncateBytes` lines 128-140 — update marker template)
- Modify: `local-gh-mcp/internal/format/github.go` (`FormatBranches`, `FormatReleases`, `FormatPRFiles`, plus add `limit` parameter and overflow detection to: `FormatPRListItem` callers, `FormatIssueListItem` callers, `FormatRunList`, `FormatCaches`, `FormatComments`, `FormatReviews`, `FormatReviewComments`)
- Modify: `local-gh-mcp/internal/gh/gh.go` (add `limit+1` to: `ListPRs`, `ListIssues`, `ListRuns`, `ListCaches`, `IssueComments`, `PRComments`, `PRReviews`, `PRReviewComments`)
- Modify: `local-gh-mcp/internal/tools/branch.go:50`, `release.go:60`, `cache.go:104` (handler-level empty-result short-circuits with `"No <things> found."`)
- Modify: every list handler that delegates rendering to a list-aware formatter to pass `limit` through
- Test: corresponding `*_test.go` files

**Acceptance Criteria:**

- All top-level list tools (`gh_list_prs`, `gh_list_issues`, `gh_list_runs`, `gh_list_caches`, `gh_list_branches`, `gh_list_releases`, `gh_list_pr_files`, `gh_list_pr_comments`, `gh_list_issue_comments`, `gh_list_pr_reviews`, `gh_list_pr_review_comments`) emit `"No <things> found."` (handler-returned) when empty.
- All list endpoints fetch `limit+1` items (REST `per_page`, gh CLI `--limit`, or jq `[:limit+1]`). When the formatter receives more than `limit` items, it slices to `limit` and emits the trailer `\n[truncated — showing N of M <things>]\n`.
- Unified truncation marker template `[truncated — showing X of Y <unit>]` is used everywhere. `TruncateBody` and `TruncateBytes` adopt it (TruncateBody now reports `bytes`, not `chars`, since it operates on `len(s)`). The godoc comment for `TruncateBody` uses `—` not `--`.
- Tests verify: empty cases, no-overflow cases, overflow-by-one cases, large-overflow cases, marker text exact match.

**Notes:** Resolves findings #7, #8, #9, and transitively #15. Sub-list formatters (`FormatComments`, `FormatReviews`, `FormatReviewComments`, `FormatCheckList`) still use the terse `"No X."` form for empty since they render embedded inline — only the handler's top-level path needs `"No X found."`. This is a large mechanical change. Run `make audit` once at the end of the task to catch regressions in any list tool you didn't directly think about.

@test-driven-development

**Commit:** `refactor(local-gh-mcp): standardize empty-result and truncation markers`

---

### Task 6: Validation tightening for string IDs and helper consolidation

**Files:**

- Modify: `local-gh-mcp/internal/tools/tools.go` (add `requirePositiveIntString` near line 207; remove `intFromArgsOr` at lines 194-202)
- Modify: `local-gh-mcp/internal/tools/run.go` (`handleViewRun` lines 229-232; `handleRerunRun` lines 256-259; `handleCancelRun` lines 274-277; `handleViewRunJobLogs` lines 291-296)
- Modify: `local-gh-mcp/internal/tools/cache.go` (`handleDeleteCache` line 113-115)
- Modify: `local-gh-mcp/internal/tools/release.go:70` (replace `intFromArgsOr` call with `intFromArgs`)
- Test: `local-gh-mcp/internal/tools/tools_test.go`, `local-gh-mcp/internal/tools/run_test.go`, `local-gh-mcp/internal/tools/cache_test.go`

**Acceptance Criteria:**

- New helper `requirePositiveIntString(args map[string]any, key string) (string, *gomcp.CallToolResult)` returns trimmed string on success; rejects missing, empty, whitespace, non-numeric, zero, negative with `"<key> must be a positive integer"`-style error. Parse via `strconv.ParseInt`.
- `handleViewRun`, `handleRerunRun`, `handleCancelRun` use `requirePositiveIntString(args, "run_id")`; `handleDeleteCache` uses it for `cache_id`.
- `handleViewRunJobLogs` job_id block (current lines 291-296) replaced with `jobIDInt, errResult := requirePositiveInt(args, "job_id"); if errResult != nil { return errResult, nil }`.
- `intFromArgsOr` deleted; only callsite (release.go:70) switched to `intFromArgs(args, "max_body_length")` (relies on `clampMaxBodyLength(0)` returning default).
- Tests cover: rejects missing/empty/whitespace/zero/negative/non-numeric; accepts large positive int strings.

**Notes:** Resolves findings #14 and #27. The string-ID contract stays (precision-safe for int64). Validation is the only thing tightening.

@test-driven-development

**Commit:** `fix(local-gh-mcp): tighten string-id validation and drop dup int helper`

---

### Task 7: Format polish — small cross-cutting render fixes

**Files:**

- Modify: `local-gh-mcp/internal/format/github.go`:
  - `FormatPRView` lines 244-247 — `(none)` fallback for `ReviewDecision`
  - `writeReviewCommentThread` lines 386-411 — append `[ASSOC]` to header
  - `ReviewComment` struct line 74 — add `AuthorAssociation string \`json:"author_association"\``
  - `FormatCheckList` description (no code change here — the schema description is in tools/pr.go; this entry is a placeholder reminder)
  - `FormatReleases` lines 619-651 — render `by @user` via `FormatAuthor`
  - `FormatCaches` line 562 — render ID with backticks: `- \`%d\` \`%s\` — …`
  - `FormatSearchCodeItem` lines 504-516 — collapse fragment whitespace via `strings.Fields`
- Modify: `local-gh-mcp/internal/gh/gh.go`:
  - `PRReviewComments` jq projection at line 504 — add `author_association`
  - `ListReleases` projection at line 797 — add `author` to `--json` field list
- Modify: `local-gh-mcp/internal/tools/context.go:44` — `Logged in as @%s` instead of `\`%s\``
- Modify: `local-gh-mcp/internal/tools/pr.go:352` — checks description: `"…with state (e.g. SUCCESS, FAILURE, PENDING) and link."`
- Test: corresponding `*_test.go` files

**Acceptance Criteria:**

- `gh_view_pr` output renders `**Review:** (none)` when `ReviewDecision` is empty (no trailing space).
- `gh_list_pr_review_comments` headers render `@user [MEMBER]` / `@user [CONTRIBUTOR]` for non-NONE associations.
- `gh_list_releases` rows include `by @<author>` between title and date; bot authors flattened correctly via Task 4 helper.
- `gh_list_caches` row IDs render in backticks: `- \`12345\` \`my-key\` …`.
- `gh_search_code` fragments are single-line (no embedded tabs/newlines).
- `gh_whoami` output starts with `Logged in as @<login>` (with optional `[bot]`).
- `gh_list_pr_checks` description text matches the new template.
- Tests updated for each render change.

**Notes:** Resolves findings #12, #16, #19, #20, #22, #23, #28. All independent micro-edits; bundling because each is too small for its own task. Run `make test` once at the end to catch any test-string mismatches.

@test-driven-development

**Commit:** `chore(local-gh-mcp): cross-cutting render polish`

---

### Task 8: Schema/description cleanups

**Files:**

- Modify: `local-gh-mcp/internal/tools/issue.go` (max_body_length descriptions at lines 38, 141)
- Modify: `local-gh-mcp/internal/tools/pr.go` (max_body_length at lines 105, 425, 459, 493; state enum description for `gh_list_prs` and `gh_search_prs` — add closed-excludes-merged note; `gh_list_prs` description — add draft filter pointer)
- Modify: `local-gh-mcp/internal/tools/release.go:37` (max_body_length description — add max value)
- Modify: `local-gh-mcp/internal/tools/run.go:36-37` (status enum description — split lifecycle vs conclusion)
- Modify: `local-gh-mcp/internal/tools/run.go:285-303` (`handleViewRunJobLogs` prepends `# Job <job_id> logs (last <tail> lines)\n\n` header before TruncateBytes)
- Modify: `local-gh-mcp/internal/tools/run.go:223-247` (`handleViewRun` log_failed path prepends `# Run <run_id> failed-job logs (last <tail> lines per job)\n\n`)
- Test: `local-gh-mcp/internal/tools/tools_test.go` (existing schema-validation test), `run_test.go`

**Acceptance Criteria:**

- All `max_body_length` descriptions standardized to one of:
  - `"Max body length in bytes (default 2000, max 50000)."` (single body)
  - `"Max body length per <comment|review> in bytes (default 2000, max 50000)."` (per-element)
  - `"Max release-notes body length in bytes (default 2000, max 50000)."` (release)
- `gh_list_prs` description gains: `" To filter by draft status, use gh_search_prs with 'is:draft' (or '-is:draft')."` appended.
- `gh_list_prs.state` description and `gh_search_prs.state` description gain: `" Note: 'closed' excludes merged PRs; use 'merged' explicitly to include them."`
- `gh_list_runs.status` description: `"Filter by workflow run status (lifecycle: queued, in_progress, completed, requested, waiting) or conclusion (success, failure, cancelled, skipped, neutral, action_required, stale, startup_failure, timed_out)."`
- `gh_view_run_job_logs` output begins with `# Job <id> logs (last <tail> lines)\n\n`.
- `gh_view_run` log_failed output begins with `# Run <id> failed-job logs (last <tail> lines per job)\n\n`.
- Existing tests for these tools still pass; new tests assert log header presence and shape.

**Notes:** Resolves findings #11, #17, #18, #21, #25, and (transitively) #26. Pure schema/description and presentation changes. The header is added in the handler, not in `gh.go`.

**Commit:** `docs(local-gh-mcp): standardize schema descriptions and add log anchors`

---

### Task 9: Search PR/issue body excerpts and projection cleanup

**Files:**

- Modify: `local-gh-mcp/internal/format/github.go`:
  - `SearchPRItem` struct lines 168-176 — add `Body string \`json:"body"\``, drop unused intent of `createdAt`/`url` (already not in struct)
  - `SearchIssueItem` struct lines 178-187 — same
  - `FormatSearchPRItem` lines 484-489 — render second line `> <truncated body excerpt>` when body non-empty (newlines collapsed to single spaces, truncated to `maxBodyLength`)
  - `FormatSearchIssueItem` lines 491-496 — same
- Modify: `local-gh-mcp/internal/gh/gh.go` lines 372-378:
  - `searchPRFields = "number,title,state,author,repository,body,updatedAt"`
  - `searchIssueFields = "number,title,state,author,repository,body,updatedAt"`
  - (Drop `url`, `createdAt`; add `body`.)
- Modify: `local-gh-mcp/internal/tools/search.go`:
  - `gh_search_prs`/`gh_search_issues` schemas gain `max_body_length` property: `{"type": "number", "default": 200, "description": "Max body excerpt length in bytes (default 200, max 500)."}`
  - Add `clampSearchBodyLength(v int) int` helper in `tools.go` capping at 500, defaulting to 200.
  - Handlers pass clamped `maxBodyLength` to formatters.
- Test: `search_test.go`, `format/github_test.go`, `gh/gh_test.go`

**Acceptance Criteria:**

- `searchPRFields` and `searchIssueFields` are exactly as listed above (drop `url` and `createdAt`, add `body`).
- `FormatSearchPRItem(item, maxBody)` renders one bullet line plus, when `item.Body` is non-empty, a second indented line of the form `  > <excerpt>` where the excerpt has all whitespace runs collapsed to single spaces and is truncated to `maxBody` bytes. When body is empty, no second line.
- Same shape for `FormatSearchIssueItem`.
- `gh_search_prs` and `gh_search_issues` accept `max_body_length` (default 200, max 500); excerpt is rendered with the clamped value.
- Tests cover: empty body (no second line); short body (full); long body (truncated).

**Notes:** Resolves findings #13 and #24. Smaller body cap than view tools because search returns up to 100 items; 500×100 = 50KB ceiling. Truncation marker on the excerpt should match Task 5's unified template.

@test-driven-development

**Commit:** `feat(local-gh-mcp): show body excerpts in PR/issue search results`

---

### Task 10: Search filter conflict rejection

**Files:**

- Modify: `local-gh-mcp/internal/tools/search.go` (all five handlers; add helper)
- Test: `local-gh-mcp/internal/tools/search_test.go`

**Acceptance Criteria:**

- New unexported helper:
  ```go
  func containsQualifier(tokens []string, qualifiers ...string) bool {
      for _, t := range tokens {
          for _, q := range qualifiers {
              if strings.HasPrefix(t, q+":") {
                  return true
              }
          }
      }
      return false
  }
  ```
  Operates on the already-shlex-split tokens (re-use `splitSearchQuery` from `gh.go` — expose if not already accessible, or inline the split locally).
- Per-handler conflict detection runs after enum validation but before any state translation (Task 2). Mapping:
  - `gh_search_prs` / `gh_search_issues`: `state` ↔ {`state`, `is`}, `repo` ↔ `repo`, `owner` ↔ {`owner`, `org`}, `author` ↔ `author`, `label` ↔ `label`
  - `gh_search_repos`: `owner` ↔ {`owner`, `org`, `user`}, `language` ↔ `language`, `topic` ↔ `topic`, `stars` ↔ `stars`
  - `gh_search_code`: `repo` ↔ `repo`, `owner` ↔ {`owner`, `org`}, `language` ↔ `language`, `extension` ↔ `extension`, `filename` ↔ `filename`
  - `gh_search_commits`: `repo` ↔ `repo`, `owner` ↔ {`owner`, `org`}, `author` ↔ `author`
- Conflict produces a `gomcp.NewToolResultError` with exact message `"<flag> set both via flag and query; pick one"`.
- All five tool description strings drop the `"behavior on conflict varies by search type, so pick one place to set each filter"` clause and replace with `"setting the same filter via both flag and query is rejected"`.
- Tests cover: each tool, one conflict per qualifier mapping, plus a no-conflict baseline.

**Notes:** Resolves finding #10. Order with Task 2: conflict detection runs before `is:merged` injection so that `state=merged` + `query` containing `is:` correctly errors.

@test-driven-development

**Commit:** `feat(local-gh-mcp): reject duplicate filters in search tools`

---

### Task 11: Documentation update and audit-doc cleanup

**Files:**

- Modify: `local-gh-mcp/CLAUDE.md` — Conventions section
- Delete: `local-gh-mcp/readonly-tools-audit.md`

**Acceptance Criteria:**

- `local-gh-mcp/CLAUDE.md` body-truncation bullet updated to reflect Task 5 marker change: `"Body truncation: tools returning text bodies accept max_body_length param (default 2000, max 50000). Bodies exceeding the limit are cut on a whitespace boundary with [truncated — showing N of M bytes]."`
- New bullet added: `"List truncation: list tools fetch limit+1 items so the formatter can detect overflow. When detected, the formatter slices to limit and appends [truncated — showing N of M <things>]."`
- New bullet added: `"Author rendering: FormatAuthor strips a literal [bot] suffix from the login as a defensive fallback for endpoints that don't populate is_bot."`
- The audit findings file `local-gh-mcp/readonly-tools-audit.md` is removed.

**Notes:** No DESIGN.md or README.md changes — those describe intended behavior and tool surface, neither of which changes. Pure CLAUDE.md cleanup so future sessions know the new conventions.

**Commit:** `docs(local-gh-mcp): update conventions for new truncation/list patterns`

---

<!-- Documentation updates needed: only CLAUDE.md (Task 11). README.md and DESIGN.md cover intended behavior and tool surface, which the audit fixes don't change. -->
