# local-gh-mcp Phase 3 — Design

Date: 2026-04-23

## Context

Phase 3 of the `local-gh-mcp` improvements tracked in `.designs/2026-04-21-local-gh-mcp-improvements.md`. Phases 1 (schema polish) and 2 (renames) are already shipped on branch `improve-gh-mcp`. This phase adds new tools, new filters, and removes one overlapping parameter. Breaking changes are acceptable (sole consumer is `mcp-broker`).

## Goals

1. Close the biggest capability gaps flagged in the original review: identity grounding, PR state transitions, per-job CI logs, release metadata, branch listing.
2. Eliminate list/search tool overlap that confuses agents.
3. Stay within the tool-count envelope implied by the original design (~38 tools), with one scope cut bringing us to 37.

## Scope Revisions vs Original Phase 3

Two items from the original Phase 3 plan are dropped after section-by-section review. Both have been moved to the "Skipped" section of `.designs/2026-04-21-local-gh-mcp-improvements.md`.

**#10d — `sort` / `order` on `gh_list_prs` and `gh_list_issues` — dropped.** `gh` doesn't expose these flags natively. Implementation would require constructing `--search` DSL fragments internally (conflicts with #10c's "no overlap" principle) or switching these paths to `gh api` (new plumbing for a niche feature). `gh_search_prs` / `gh_search_issues` already serve sorted queries via the DSL (`sort:created-asc`) — route agents there instead.

**#11b — `gh_view_file` — dropped.** Sandboxed agents already have the repo checked out; file reads are `cat <path>` or `git show <ref>:<path>`. The tool's hidden edge cases (binary detection, LFS pointers, encoding, symlinks) don't pay for themselves. Revisit if a concrete cross-repo use case emerges.

## Tool Count Delta

- **Before Phase 3:** 28 tools
- **After Phase 3:** 37 tools (+9 new)
- **New tools:** `gh_whoami`, `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`, `gh_list_pr_files`, `gh_list_branches`, `gh_view_run_job_logs`, `gh_list_releases`, `gh_view_release`
- **Breaking param removal:** `search` on `gh_list_prs` and `gh_list_issues`
- **Additive params:** `actor`, `event` on `gh_list_runs`

## New Tools

### `gh_whoami` — authenticated user identity

- **Command:** `gh api /user`
- **Params:** none
- **Category:** new `context` — new file `internal/tools/context.go`, mirroring `github/github-mcp-server`'s context toolset. Reserved for future identity-adjacent tools (notifications, assigned items) without polluting PR/issue categories.
- **Output:** 2-line markdown

  ```
  Logged in as `<login>` (<name>)
  <html_url>
  ```

  - `name` omitted if null
  - ` [bot]` appended after login if `type == "Bot"`
  - Fields sourced from the API response: `login`, `name`, `html_url`, `type`

- **No caching** — the call is cheap.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.
- **Unlocks:** `author:@me` / `review-requested:@me` search queries by grounding the agent in "who am I via this server."

### `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr` — PR state transitions

Three tools in `internal/tools/pr.go` alongside `gh_close_pr`.

| Tool           | Command                                        | Confirmation output                                 |
| -------------- | ---------------------------------------------- | --------------------------------------------------- |
| `gh_ready_pr`  | `gh pr ready <pr_number> -R owner/repo`        | `PR #<n> in <owner>/<repo> marked ready for review` |
| `gh_draft_pr`  | `gh pr ready <pr_number> --undo -R owner/repo` | `PR #<n> in <owner>/<repo> converted to draft`      |
| `gh_reopen_pr` | `gh pr reopen <pr_number> -R owner/repo`       | `PR #<n> in <owner>/<repo> reopened`                |

- **Params (all three):** `owner`, `repo`, `pr_number` — all required.
- **Error handling:** pass `gh` errors through unchanged. If the PR is already in the target state, the agent sees `gh`'s message ("already ready", "already open"). `idempotentHint: true` is a retry-safety hint; it does not obligate the server to swallow state-mismatch errors.
- **Annotations:** `readOnlyHint: false`, `destructiveHint: false`, `idempotentHint: true`, `openWorldHint: true`.

### `gh_list_pr_files` — files a PR touches

- **Command:** `gh api repos/O/R/pulls/N/files`
- **Params:** `owner`, `repo`, `pr_number` (required); `limit` (default 30, max 100)
- **Output:** markdown bullet list with `+`/`-` counts and status:

  ```
  - `path/to/file.go` — +12/-3 (modified)
  ```

  Truncation trailer on overflow: `[truncated — showing 30 of 82 files]`.

- **Placement:** existing `pr.go`.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.

### `gh_list_branches` — repo branches

- **Command:** `gh api repos/O/R/branches`
- **Params:** `owner`, `repo` (required); `limit` (default 30, max 100)
- **Output:** markdown bullet list with last-commit SHA:

  ```
  - `main` (abc1234)
  ```

  Truncation trailer on overflow.

- **Skipped:** `protected` filter (niche; agents can post-filter the output).
- **Placement:** new `branch.go`.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.

### `gh_view_run_job_logs` — per-job CI logs

- **Command:** `gh run view --job <job_id> --log -R owner/repo`
- **Params:** `owner`, `repo`, `job_id` (required); `tail_lines` (default 500, max 5000)
- **Output:** raw log text, last `tail_lines` lines.
- **Job ID discovery:** no separate list tool. Implementation note: verify `gh_view_run`'s current output already surfaces job IDs per job; extend its output if not.
- **Skipped:** `run_id` param (redundant — job IDs are globally unique), `failed_only` filter (per-job is already targeted).
- **Placement:** existing `run.go`.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.

### `gh_list_releases` — release metadata

- **Command:** `gh release list -R owner/repo`
- **Params:** `owner`, `repo` (required); `limit` (default 30, max 100)
- **Output:** markdown bullet list (newest first):

  ```
  - `v1.2.3` — "Feature X" (published 2026-04-15) [draft]
  ```

- **Skipped:** draft / pre-release filters (default `gh` behavior shows everything, which matches typical triage use).
- **Placement:** new `release.go`.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.

### `gh_view_release` — single release

- **Command:** `gh release view [<tag>] -R owner/repo` (omit tag → `--latest`)
- **Params:** `owner`, `repo` (required); `tag` (optional; defaults to latest release); `max_body_length` (existing convention, default 2000)
- **Output:** markdown with tag, name, author, published date, release body (length-capped), asset list.
- **Asset rendering:** name + size only. No download URLs (sandboxed agents typically can't fetch arbitrary URLs, and signed URLs expire quickly).
- **Placement:** `release.go`.
- **Annotations:** `readOnlyHint: true`, `openWorldHint: true`.

## Parameter Changes on Existing Tools

### `gh_list_runs` — new filters (#10f)

- **`actor`** — free-form string (GitHub login). Passes through to `gh run list --user`. No validation.
- **`event`** — free-form string. Passes through to `gh run list --event`. No enum (40+ webhook event types and new ones ship over time). Description lists the common handful: `push`, `pull_request`, `schedule`, `workflow_dispatch`.
- **Skipped:** `created` filter (the DSL `>=YYYY-MM-DD` is awkward without broader search-DSL context).

### `gh_list_prs` / `gh_list_issues` — remove `search` param (#10c, breaking)

- Drop `search` from both tools. Clean removal — no deprecation window.
- Phase 1's sharpened descriptions already frame list tools as "use when you have owner/repo" and route DSL queries to `gh_search_prs` / `gh_search_issues`. Verify wording during implementation and nudge if needed.
- Tests exercising `search` on list tools: remove, or migrate to the search-tool tests if they specifically verified DSL behavior.

## Testing

- Per-tool handler tests following existing patterns in `pr_test.go` / `run_test.go` / `cache_test.go`.
- New `context_test.go`, `branch_test.go`, `release_test.go` for tools in new files.
- Update `tools_test.go` annotation-presence and schema-guard tests (established in Phase 1) to cover the new tools.
- Removal of `search` from list tools: adjust or drop existing list-tool tests that referenced it.

## Implementation Ordering

Each step is independently shippable; order minimizes rework.

1. `gh_whoami` + `context` category — lowest risk, new file, no cross-cutting.
2. PR state transitions: `gh_ready_pr`, `gh_draft_pr`, `gh_reopen_pr`.
3. `gh_list_pr_files` — existing `pr.go`, near other PR read tools.
4. `gh_list_branches` — new `branch.go`.
5. `gh_view_run_job_logs` — includes job-ID-in-`gh_view_run` verification.
6. `gh_list_releases` + `gh_view_release` — new `release.go`.
7. `gh_list_runs` `actor` / `event` filters.
8. Remove `search` param from `gh_list_prs` / `gh_list_issues` — breaking; land last so preceding additive work ships independently even if this is revisited.

## Non-Goals (Phase 3)

Deferred to Phase 4:

- Output caps on `gh_diff_pr` / `gh_view_run` (#10a)
- `gh_create_pr` accept empty body (#14a)
- `gh_list_issue_comments` `since` filter (#14b)

Out of scope entirely (see "Skipped" in `.designs/2026-04-21-local-gh-mcp-improvements.md` for rationale):

- Any "medium-value" tools from the original review: commits, workflows, notifications, code scanning alerts.
