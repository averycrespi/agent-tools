# local-gh-mcp Design

## Motivation

AI coding agents in sandboxes need to interact with GitHub — creating PRs, checking CI status, reading issues, debugging workflow failures. The official GitHub MCP server requires either OAuth (which needs GitHub App installation at user/org level) or personal access tokens (another secret to manage and distribute to clients).

Meanwhile, the host machine already has `gh` CLI authenticated and working. local-gh-mcp reuses this existing authentication by running a stdio MCP server on the host that shells out to `gh`, just as local-git-mcp reuses the host's git credentials.

## Architecture

Stdio MCP server. No network listener, no config file, no state. A caller spawns it as a subprocess and communicates over stdin/stdout using the MCP protocol.

```
Agent (sandbox) ──stdio──> mcp-broker ──stdio──> local-gh-mcp ──exec──> gh CLI
                                                                         │
                                                                    GitHub API
                                                                   (host's auth)
```

### Startup auth check

On startup, local-gh-mcp runs `gh auth status` before creating the MCP server. If the `gh` CLI is not authenticated, the server exits immediately with a clear error:

```
fatal: gh CLI is not authenticated — run 'gh auth login' first
```

This fast-fail prevents serving tools that would all fail with auth errors.

### Repo targeting

Unlike local-git-mcp (which uses `repo_path` to a local clone), local-gh-mcp uses explicit `owner` and `repo` string parameters. These map to `gh`'s `-R owner/repo` flag. No local clone is required.

Both `owner` and `repo` are validated to contain only `[a-zA-Z0-9._-]` characters before use.

## Tools

28 tools organized into 5 categories. All tools use verb-first naming with a `gh_` prefix for namespace safety.

### PR Tools (13)

| Tool                         | Description                                             | gh command                          |
| ---------------------------- | ------------------------------------------------------- | ----------------------------------- |
| `gh_create_pr`               | Create a pull request                                   | `gh pr create`                      |
| `gh_view_pr`                 | View PR metadata and description as structured markdown | `gh pr view`                        |
| `gh_list_prs`                | List PRs as markdown bullets                            | `gh pr list`                        |
| `gh_diff_pr`                 | View the diff with file summary table                   | `gh pr diff`                        |
| `gh_comment_pr`              | Add a comment to a PR                                   | `gh pr comment`                     |
| `gh_review_pr`               | Submit a review (approve, request changes, or comment)  | `gh pr review`                      |
| `gh_merge_pr`                | Merge a PR                                              | `gh pr merge`                       |
| `gh_edit_pr`                 | Edit PR metadata                                        | `gh pr edit`                        |
| `gh_list_pr_checks`          | View CI/status checks as markdown bullet list           | `gh pr checks`                      |
| `gh_close_pr`                | Close a PR                                              | `gh pr close`                       |
| `gh_list_pr_comments`        | List PR conversation (issue-style) comments as markdown | `gh pr view --json comments`        |
| `gh_list_pr_reviews`         | List top-level review submissions with state and body   | `gh pr view --json reviews`         |
| `gh_list_pr_review_comments` | List inline diff comments, grouped by file and threaded | `gh api repos/O/R/pulls/N/comments` |

### Issue Tools (4)

| Tool                     | Description                                                | gh command                      |
| ------------------------ | ---------------------------------------------------------- | ------------------------------- |
| `gh_view_issue`          | View issue metadata and description as structured markdown | `gh issue view`                 |
| `gh_list_issues`         | List issues as markdown bullets                            | `gh issue list`                 |
| `gh_comment_issue`       | Add a comment to an issue                                  | `gh issue comment`              |
| `gh_list_issue_comments` | List issue comments as markdown                            | `gh issue view --json comments` |

### Workflow Run Tools (4)

| Tool            | Description                             | gh command      |
| --------------- | --------------------------------------- | --------------- |
| `gh_list_runs`  | List workflow runs with filters         | `gh run list`   |
| `gh_view_run`   | View run details and logs               | `gh run view`   |
| `gh_rerun_run`  | Rerun a failed or specific workflow run | `gh run rerun`  |
| `gh_cancel_run` | Cancel an in-progress workflow run      | `gh run cancel` |

### Cache Tools (2)

| Tool              | Description                | gh command        |
| ----------------- | -------------------------- | ----------------- |
| `gh_list_caches`  | List GitHub Actions caches | `gh cache list`   |
| `gh_delete_cache` | Delete a cache entry       | `gh cache delete` |

### Search Tools (5)

| Tool                | Description                        | gh command          |
| ------------------- | ---------------------------------- | ------------------- |
| `gh_search_prs`     | Search pull requests across GitHub | `gh search prs`     |
| `gh_search_issues`  | Search issues across GitHub        | `gh search issues`  |
| `gh_search_repos`   | Search repositories across GitHub  | `gh search repos`   |
| `gh_search_code`    | Search code across GitHub          | `gh search code`    |
| `gh_search_commits` | Search commits across GitHub       | `gh search commits` |

## Tool Parameters

**Required** parameters are in bold. All tools with list/search semantics accept an optional `limit` parameter (see "Limits" section below).

### PR Tools

| Tool                         | Required                          | Optional                                                                                                       |
| ---------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `gh_create_pr`               | **owner, repo, title, body**      | base, head, draft, labels, reviewers, assignees                                                                |
| `gh_view_pr`                 | **owner, repo, pr_number**        | max_body_length                                                                                                |
| `gh_list_prs`                | **owner, repo**                   | state, author, label, base, head, search, limit                                                                |
| `gh_diff_pr`                 | **owner, repo, pr_number**        | —                                                                                                              |
| `gh_comment_pr`              | **owner, repo, pr_number, body**  | —                                                                                                              |
| `gh_review_pr`               | **owner, repo, pr_number, event** | body                                                                                                           |
| `gh_merge_pr`                | **owner, repo, pr_number**        | method (merge/squash/rebase), delete_branch, auto                                                              |
| `gh_edit_pr`                 | **owner, repo, pr_number**        | title, body, base, add_labels, remove_labels, add_reviewers, remove_reviewers, add_assignees, remove_assignees |
| `gh_list_pr_checks`          | **owner, repo, pr_number**        | —                                                                                                              |
| `gh_close_pr`                | **owner, repo, pr_number**        | comment                                                                                                        |
| `gh_list_pr_comments`        | **owner, repo, pr_number**        | max_body_length, limit                                                                                         |
| `gh_list_pr_reviews`         | **owner, repo, pr_number**        | max_body_length, limit                                                                                         |
| `gh_list_pr_review_comments` | **owner, repo, pr_number**        | max_body_length, limit                                                                                         |

### Issue Tools

| Tool                     | Required                            | Optional                                                 |
| ------------------------ | ----------------------------------- | -------------------------------------------------------- |
| `gh_view_issue`          | **owner, repo, issue_number**       | max_body_length                                          |
| `gh_list_issues`         | **owner, repo**                     | state, author, assignee, label, milestone, search, limit |
| `gh_comment_issue`       | **owner, repo, issue_number, body** | —                                                        |
| `gh_list_issue_comments` | **owner, repo, issue_number**       | max_body_length, limit                                   |

### Workflow Run Tools

| Tool            | Required                | Optional                        |
| --------------- | ----------------------- | ------------------------------- |
| `gh_list_runs`  | **owner, repo**         | branch, status, workflow, limit |
| `gh_view_run`   | **owner, repo, run_id** | log_failed                      |
| `gh_rerun_run`  | **owner, repo, run_id** | failed_only                     |
| `gh_cancel_run` | **owner, repo, run_id** | —                               |

### Cache Tools

| Tool              | Required                  | Optional           |
| ----------------- | ------------------------- | ------------------ |
| `gh_list_caches`  | **owner, repo**           | limit, sort, order |
| `gh_delete_cache` | **owner, repo, cache_id** | —                  |

### Search Tools

Search tools use `query` as the primary search string rather than `owner/repo`, since they operate across repositories.

| Tool                | Required  | Optional                                          |
| ------------------- | --------- | ------------------------------------------------- |
| `gh_search_prs`     | **query** | repo, owner, state, author, label, limit          |
| `gh_search_issues`  | **query** | repo, owner, state, author, label, limit          |
| `gh_search_repos`   | **query** | owner, language, topic, stars, limit              |
| `gh_search_code`    | **query** | repo, owner, language, extension, filename, limit |
| `gh_search_commits` | **query** | repo, owner, author, limit                        |

## Limits

All tools with a `limit` parameter enforce default and maximum values:

- **Default limit:** 30 results
- **Maximum limit:** 100 results

If a caller requests more than 100, the value is clamped to 100.

### Derivation

These values are grounded in the `gh` CLI's own defaults and the GitHub API's constraints:

- **Default of 30:** The `gh` CLI defaults to 30 for `gh pr list`, `gh issue list`, and all `gh search` commands. `gh run list` defaults to 20. We use 30 as a consistent default across all tools.
- **Maximum of 100:** The GitHub REST API returns at most 100 items per page. While `gh` can fetch multiple pages (up to 1000 for some commands), a single API page keeps responses fast and prevents context window bloat for agents.

### No pagination

Pagination is deliberately not supported. If 100 results isn't enough, the query is too broad — agents should refine their search/filter parameters rather than paginate through large result sets. This matches how agents actually work: they narrow queries to find specific items, not scroll through lists.

## Output Format

All read tools return **structured markdown** instead of raw JSON. The `gh` CLI's `--json` output is parsed server-side into Go structs, then formatted as human/LLM-readable markdown. Write tools (create, comment, merge, edit, close, rerun, cancel, delete) return plain text confirmations.

### Formatting patterns

- **Author flattening**: all author objects rendered as `@login` or `@login [bot]` — internal IDs and display names are dropped
- **Body truncation**: tools returning text bodies accept a `max_body_length` param (default 2000, max 50000). Bodies exceeding the limit are cut on a whitespace boundary with `[truncated — N/M chars shown]`
- **View tools** (`gh_view_pr`, `gh_view_issue`): markdown header with labeled metadata fields, followed by truncated description
- **List/search tools**: markdown bullet per item with key fields inline
- **Diff tool** (`gh_diff_pr`): file summary table (file names, +/- counts) prepended before the raw unified diff
- **Check tool** (`gh_list_pr_checks`): flat markdown bullet list; FAILURE/ERROR include link
- **Comment tools** (`gh_list_pr_comments`, `gh_list_issue_comments`): headed blocks per comment; minimized/spam comments show `[minimized: REASON]`; images replaced with `[image]`
- **Review list** (`gh_list_pr_reviews`): headed blocks per review showing state (APPROVED/CHANGES_REQUESTED/COMMENTED/DISMISSED), author, date; empty bodies rendered as `(no body)`
- **Review comment list** (`gh_list_pr_review_comments`): grouped by file path; threaded by `in_reply_to_id` with indented replies; falls back to `original_line` when `line` is null (outdated comments)
- **Run view** (`gh_view_run`): structured header + job list; `log_failed=true` returns raw logs unchanged

## Project Structure

```
local-gh-mcp/
├── cmd/local-gh-mcp/
│   ├── main.go              # Entry point
│   └── root.go              # Cobra root cmd + MCP server setup + auth check
├── internal/
│   ├── exec/
│   │   ├── runner.go        # Runner interface for command execution
│   │   └── runner_test.go
│   ├── format/
│   │   ├── format.go        # Core helpers (author, date, truncation, diff, images, labels)
│   │   ├── format_test.go
│   │   ├── github.go        # GitHub struct types + markdown formatters
│   │   └── github_test.go
│   ├── gh/
│   │   ├── gh.go            # GH client wrapping exec.Runner
│   │   └── gh_test.go
│   └── tools/
│       ├── tools.go         # AllTools() registration + shared helpers
│       ├── pr.go            # PR tool definitions + handlers
│       ├── issue.go         # Issue tool definitions + handlers
│       ├── run.go           # Workflow run tool definitions + handlers
│       ├── cache.go         # Cache tool definitions + handlers
│       ├── search.go        # Search tool definitions + handlers
│       └── *_test.go        # Tests per file
├── Makefile
├── CLAUDE.md
├── DESIGN.md
├── README.md
├── go.mod
└── go.sum
```

Tools are split into separate files by category (unlike local-git-mcp which has 5 tools in one file) since we have 28 tools.

## GH Client Layer

The `internal/gh` package provides a typed Go interface over the `gh` CLI:

```go
type Client interface {
    // PR operations
    CreatePR(ctx context.Context, owner, repo string, opts CreatePROpts) (string, error)
    ViewPR(ctx context.Context, owner, repo string, number int) (string, error)
    ListPRs(ctx context.Context, owner, repo string, opts ListPROpts) (string, error)
    DiffPR(ctx context.Context, owner, repo string, number int) (string, error)
    CommentPR(ctx context.Context, owner, repo string, number int, body string) (string, error)
    ReviewPR(ctx context.Context, owner, repo string, number int, event, body string) (string, error)
    MergePR(ctx context.Context, owner, repo string, number int, opts MergePROpts) (string, error)
    EditPR(ctx context.Context, owner, repo string, number int, opts EditPROpts) (string, error)
    CheckPR(ctx context.Context, owner, repo string, number int) (string, error)
    ClosePR(ctx context.Context, owner, repo string, number int, comment string) (string, error)

    // Issue operations
    ViewIssue(ctx context.Context, owner, repo string, number int) (string, error)
    ListIssues(ctx context.Context, owner, repo string, opts ListIssuesOpts) (string, error)
    CommentIssue(ctx context.Context, owner, repo string, number int, body string) (string, error)

    // Comment listing operations
    PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)
    IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error)

    // Workflow run operations
    ListRuns(ctx context.Context, owner, repo string, opts ListRunsOpts) (string, error)
    ViewRun(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error)
    Rerun(ctx context.Context, owner, repo string, runID string, failedOnly bool) (string, error)
    CancelRun(ctx context.Context, owner, repo string, runID string) (string, error)

    // Cache operations
    ListCaches(ctx context.Context, owner, repo string, opts ListCachesOpts) (string, error)
    DeleteCache(ctx context.Context, owner, repo string, cacheID string) (string, error)

    // Search operations
    SearchPRs(ctx context.Context, query string, opts SearchPRsOpts) (string, error)
    SearchIssues(ctx context.Context, query string, opts SearchIssuesOpts) (string, error)
    SearchRepos(ctx context.Context, query string, opts SearchReposOpts) (string, error)
    SearchCode(ctx context.Context, query string, opts SearchCodeOpts) (string, error)
    SearchCommits(ctx context.Context, query string, opts SearchCommitsOpts) (string, error)

    // Auth
    AuthStatus(ctx context.Context) error
}
```

Each method returns `(string, error)` where the string is the raw JSON output from `gh`. The tool handlers parse this JSON into typed structs from `internal/format/`, then call formatting functions to produce structured markdown.

## Validation and Error Handling

**Input validation:**

- Missing required parameters return an MCP tool error: `"owner is required"`
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` pattern
- `limit` values clamped to [1, 100] range
- `event` parameter for `gh_review_pr` validated against allowed values: `approve`, `request_changes`, `comment`
- `method` parameter for `gh_merge_pr` validated against: `merge`, `squash`, `rebase`
- `event`, `method`, and `state` parameters declare enums in JSON Schema and are also validated in the handler for defense-in-depth.

**Command errors:**

- `gh` CLI failures return MCP tool error with stderr output for actionable feedback
- Errors wrapped with context: `fmt.Errorf("doing X: %w", err)`

**No retries** — `gh`'s exit code and output are passed through faithfully.

## Security

**Argument injection prevention:**

- User-supplied string values (titles, bodies, comments) are passed as separate args to `exec.Command`, never interpolated into shell strings
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` before constructing `-R owner/repo`
- `--` end-of-options separator used where applicable

**No access control.** Authorization is the caller's responsibility (typically mcp-broker). local-gh-mcp trusts its caller, same as local-git-mcp.

## Tech Stack

| Component    | Library                                        |
| ------------ | ---------------------------------------------- |
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go)  |
| CLI          | [cobra](https://github.com/spf13/cobra)        |
| Logging      | `log/slog` (stdlib)                            |
| Testing      | [testify](https://github.com/stretchr/testify) |

## Design Decisions

**Stdio transport, not HTTP.** Same rationale as local-git-mcp: simpler, no port allocation, no TLS, no auth. The caller manages the process lifecycle.

**Explicit owner/repo, not repo_path.** Unlike local-git-mcp, most `gh` operations don't need a local clone. Passing `owner` and `repo` directly maps cleanly to `gh -R owner/repo` and lets agents operate on repos they haven't cloned.

**Structured markdown output.** All read tools (view, list, search, diff, check) return structured markdown instead of raw JSON. The `gh` CLI's `--json` output is parsed server-side and formatted as concise, labeled markdown that's easy for LLMs to consume without wasting tokens on JSON syntax. Write tools return plain text confirmations.

**Startup auth check.** Fail fast if `gh` isn't authenticated rather than returning auth errors on every tool call. This gives the operator immediate feedback.

**Verb-first tool naming.** `gh_create_pr` reads more naturally than `gh_pr_create`. The `gh_` prefix provides namespace safety when tools from multiple MCP servers share a flat namespace.

**No pagination.** Agents should refine queries, not paginate. The 100-item maximum (one GitHub API page) keeps responses fast and focused.

**Shell out to gh, don't use a Go GitHub client library.** The whole point is to reuse the host's existing `gh` authentication. A Go library would need its own auth configuration, defeating the purpose.

**No gh api escape hatch (initially).** Only expose focused, schema-validated tools. A raw API tool can be added later if specific use cases demand it.

## Testing

- **Unit tests** — mock `exec.Runner` in the `gh` package to verify argument construction and flag handling. Mock `Client` interface in the `tools` package to verify handler logic, parameter extraction, and error responses.
- **No integration tests initially** — would require an authenticated `gh` CLI in CI, which adds complexity. Can be added later with `//go:build integration` tag.
