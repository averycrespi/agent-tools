# Design: Structured Markdown Output for local-gh-mcp

## Problem

The local-gh-mcp MCP tools return raw JSON blobs and unstructured text that are
awkward for LLM callers to work with:

- **Nested noise**: Author objects contain internal IDs, bot flags, display names
  that callers never use. Every author is `{"id":"MDQ6...","is_bot":false,"login":"foo","name":"Foo Bar"}`.
- **Monolithic responses**: `gh_view_pr` and `gh_view_issue` return everything in
  one blob — metadata, body, comments, status checks — when the caller usually
  only needs a subset.
- **Unbounded text**: Issue/PR bodies can be thousands of characters (debug logs,
  stack traces). Comments include spam, minimized content, and reaction metadata.
- **No structure in diffs**: `gh_diff_pr` returns raw unified diff with no summary
  of which files changed or how many lines were added/removed.
- **JSON overhead**: Repeated key names, braces, brackets, and quotes waste tokens
  for no benefit when the data is being consumed by an LLM, not a program.

## Design

### Principle

Every tool returns **structured markdown** instead of raw JSON or raw text. A
formatting layer in `internal/format/` sits between `gh.Client` (which still
returns raw JSON from the `gh` CLI) and the MCP tool handlers (which currently
pass it through verbatim).

### New Tools

Two new tools decompose the monolithic view responses:

| Tool | Purpose |
|------|---------|
| `gh_list_pr_comments` | List comments on a PR (separated from `gh_view_pr`) |
| `gh_list_issue_comments` | List comments on an issue (separated from `gh_view_issue`) |

Parameters: `owner`, `repo`, `number` (required), `max_body_length` (optional),
`limit` (optional).

### Shared Parameter: `max_body_length`

Added to tools that return text bodies:
- `gh_view_pr`, `gh_view_issue`, `gh_list_pr_comments`, `gh_list_issue_comments`

| Property | Value |
|----------|-------|
| Type | integer |
| Default | 2000 |
| Minimum | 0 (no truncation marker — omit body entirely) |
| Maximum | 50000 |

When a body exceeds `max_body_length`, it is cut at the limit on a whitespace
boundary and `\n\n[truncated — 2000/8432 chars shown]` is appended.

### Formatting Patterns

#### Author Flattening

All author objects are flattened to `@login`. If `is_bot` is true, rendered as
`@login [bot]`. The internal ID and display name are dropped everywhere.

**Before:** `{"id":"MDQ6VXNlcjM5MTQwOTM4","is_bot":false,"login":"daviddl9","name":"David"}`
**After:** `@daviddl9`

#### View Tools (`gh_view_pr`, `gh_view_issue`)

Return metadata + body as labeled markdown fields. No comments (use the
dedicated comment tools for those).

Format for `gh_view_pr`:
```
# PR #10410: test: Verify default repo setting (CLOSED)

**Author:** @daviddl9 | **Base:** trunk <- test-default-repo-1739096772
**Created:** 2025-02-09 | **Updated:** 2025-02-09
**Draft:** no | **Mergeable:** MERGEABLE | **Review:** REVIEW_REQUIRED
**Labels:** (none)

## Description

This is a test PR to verify that the default repository is set
correctly when creating PRs from forks.
```

Format for `gh_view_issue`:
```
# Issue #10000: `--allow-forking=false` not interpreted correctly... (CLOSED)

**Author:** @celloza | **Labels:** bug, gh-repo
**Created:** 2024-12-03 | **Updated:** 2024-12-06
**Milestone:** (none)

## Description

[body text, truncated per max_body_length]
```

#### Comment Tools (`gh_list_pr_comments`, `gh_list_issue_comments`)

Each comment rendered as a headed block with the body truncated per
`max_body_length`:

```
## Comments (3)

### @celloza (2024-12-03)

10 000!

### @BagToad [MEMBER] (2024-12-06)

Hey @celloza - thank you for opening this up...
[truncated — 2000/1847 chars shown]

### @Andy-commits-lgtm (2024-12-06)

[minimized: SPAM]
```

Rules:
- Minimized/spam comments: show `[minimized: REASON]` instead of body
- Author association (MEMBER, COLLABORATOR, etc.) shown in brackets after login
  when not NONE
- Image markdown (`![alt](url)`) replaced with `[image]`
- Empty comment list returns `No comments.`

#### Diff Tool (`gh_diff_pr`)

Prepend a file summary table before the raw unified diff:

```
## Files changed (2)

| File | Changes |
|------|---------|
| pkg/cmd/repo/list/http.go | +1 -1 |
| pkg/cmd/repo/list/list_test.go | +3 -3 |

## Diff

diff --git a/pkg/cmd/repo/list/http.go b/pkg/cmd/repo/list/http.go
...
```

The summary is parsed from diff headers (`---`/`+++` lines) and hunk `@@` lines.
The raw diff is included in full after the summary (no truncation — diffs need to
be complete to be useful).

#### Check Tool (`gh_check_pr`)

Flat markdown bullet list (not grouped, not JSON). Each check on one line:

```
## Status Checks (9)

- check-requirements / check-requirements: SUCCESS
- close-unmet-requirements: SKIPPED
- close-from-default-branch: SKIPPED
- label-external: SKIPPED
- ready-for-review: SKIPPED
- check-requirements / close-unmet-requirements: SKIPPED
- close-no-help-wanted: SKIPPED
- label-external / label_issues: SUCCESS (https://github.com/cli/cli/actions/runs/...)
- close-from-default-branch / close-from-default-branch: SKIPPED
```

For FAILURE and ERROR states, include the link on the same line so the caller can
investigate. For SUCCESS/SKIPPED, the link is omitted to reduce noise.

#### List Tools (`gh_list_prs`, `gh_list_issues`, `gh_list_runs`)

Markdown bullets with inline labeled fields. One bullet per item.

`gh_list_prs`:
```
- **#13053** fix(repo list): use search for private visibility — @Maa-ly, OPEN, updated 2026-03-28
- **#13051** chore(deps): bump charm.land/bubbles/v2 — @dependabot [bot], OPEN, updated 2026-03-27
```

`gh_list_issues`:
```
- **#10000** `--allow-forking=false` not interpreted correctly... — @celloza, CLOSED, labels: bug/gh-repo, updated 2024-12-06
```

`gh_list_runs`:
```
- **#23696524799** Triage Scheduled Tasks — completed/success, schedule, trunk, 2026-03-28
```

#### View Run (`gh_view_run`)

Structured markdown header with job list:

```
# Run #23696524799: Triage Scheduled Tasks (completed/success)

**Event:** schedule | **Branch:** trunk | **SHA:** abc1234
**Created:** 2026-03-28 | **Updated:** 2026-03-28

## Jobs

- check-requirements: success
- build: success
- test: failure (https://github.com/...)
```

The `log_failed=true` mode continues to return raw log output unchanged — it's
already purpose-built for debugging.

#### Search Tools (`gh_search_prs`, `gh_search_issues`, `gh_search_repos`, `gh_search_code`, `gh_search_commits`)

Same bullet format as list tools, with repository included since searches span
repos.

`gh_search_prs`:
```
- **cli/cli#13053** fix(repo list): use search for private visibility — @Maa-ly, OPEN, updated 2026-03-28
```

`gh_search_repos`:
```
- **cli/cli** GitHub's official CLI — Go, 38.2k stars, updated 2026-03-28
```

`gh_search_code`:
```
- **cli/cli** pkg/cmd/repo/list/http.go — [matched text preview]
```

`gh_search_commits`:
```
- **cli/cli** abc1234 fix: handle edge case — @author, 2026-03-28
```

### Implementation

#### New Package: `internal/format/`

Helpers for constructing markdown output:

- `FormatAuthor(author)` — flatten to `@login` / `@login [bot]`
- `TruncateBody(text, maxLen)` — truncate on whitespace boundary, append marker
- `FormatDate(timestamp)` — extract `YYYY-MM-DD` from ISO timestamp
- `ParseDiffSummary(diff)` — extract file names and +/- counts from unified diff
- `StripImages(markdown)` — replace `![...](...)` with `[image]`

Each tool handler in `internal/tools/` calls these helpers to build markdown
strings instead of returning raw `gh.Client` output.

#### Changes to Existing Tools

| Tool | Change |
|------|--------|
| `gh_view_pr` | Parse JSON, format as markdown, add `max_body_length` param, remove comments from output |
| `gh_view_issue` | Parse JSON, format as markdown, add `max_body_length` param, remove comments from output |
| `gh_diff_pr` | Parse diff for file summary, prepend table |
| `gh_check_pr` | Parse JSON, format as bullet list |
| `gh_list_prs` | Parse JSON, format as bullets |
| `gh_list_issues` | Parse JSON, format as bullets |
| `gh_list_runs` | Parse JSON, format as bullets |
| `gh_view_run` | Parse JSON, format as markdown (log_failed unchanged) |
| `gh_search_*` | Parse JSON, format as bullets |

#### GH Client Changes

The `gh.Client` methods need new methods for fetching comments:
- `PRComments(ctx, owner, repo, number, limit)` — calls `gh pr view --json comments`
- `IssueComments(ctx, owner, repo, number, limit)` — already fetched by
  `IssueView` but needs a dedicated method

The existing `PRView` and `IssueView` methods can drop comments from their
`--json` field lists since comments are now served by separate tools.

#### Documentation Updates

- **CLAUDE.md**: Update architecture section to list `internal/format/` package.
  Update the "JSON output" convention to note that tools return structured
  markdown. Document the formatting and truncation conventions.
- **Tool descriptions**: Update MCP tool descriptions in `pr.go`, `issue.go`,
  etc. to briefly describe the output format and document `max_body_length`.

### Non-Goals

- **Pagination**: Still no pagination. The existing limit/clamp behavior stays.
- **Filtering diffs by file**: Not in this change. The file summary lets callers
  see what changed; they can read specific files via other tools.
- **Changing write operations**: Tools like `gh_create_pr`, `gh_merge_pr`,
  `gh_comment_pr` return simple confirmation text and don't need restructuring.
