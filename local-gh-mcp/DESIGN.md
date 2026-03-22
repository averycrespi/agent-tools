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

24 tools organized into 5 categories. All tools use verb-first naming with a `gh_` prefix for namespace safety.

### PR Tools (10)

| Tool | Description | gh command |
|------|-------------|------------|
| `gh_create_pr` | Create a pull request | `gh pr create` |
| `gh_view_pr` | View PR details as JSON | `gh pr view` |
| `gh_list_prs` | List PRs with filters | `gh pr list` |
| `gh_diff_pr` | View the diff for a PR | `gh pr diff` |
| `gh_comment_pr` | Add a comment to a PR | `gh pr comment` |
| `gh_review_pr` | Submit a review (approve, request changes, or comment) | `gh pr review` |
| `gh_merge_pr` | Merge a PR | `gh pr merge` |
| `gh_edit_pr` | Edit PR metadata | `gh pr edit` |
| `gh_check_pr` | View CI/status check results | `gh pr checks` |
| `gh_close_pr` | Close a PR | `gh pr close` |

### Issue Tools (3)

| Tool | Description | gh command |
|------|-------------|------------|
| `gh_view_issue` | View issue details as JSON | `gh issue view` |
| `gh_list_issues` | List issues with filters | `gh issue list` |
| `gh_comment_issue` | Add a comment to an issue | `gh issue comment` |

### Workflow Run Tools (4)

| Tool | Description | gh command |
|------|-------------|------------|
| `gh_list_runs` | List workflow runs with filters | `gh run list` |
| `gh_view_run` | View run details and logs | `gh run view` |
| `gh_rerun` | Rerun a failed or specific workflow run | `gh run rerun` |
| `gh_cancel_run` | Cancel an in-progress workflow run | `gh run cancel` |

### Cache Tools (2)

| Tool | Description | gh command |
|------|-------------|------------|
| `gh_list_caches` | List GitHub Actions caches | `gh cache list` |
| `gh_delete_cache` | Delete a cache entry | `gh cache delete` |

### Search Tools (5)

| Tool | Description | gh command |
|------|-------------|------------|
| `gh_search_prs` | Search pull requests across GitHub | `gh search prs` |
| `gh_search_issues` | Search issues across GitHub | `gh search issues` |
| `gh_search_repos` | Search repositories across GitHub | `gh search repos` |
| `gh_search_code` | Search code across GitHub | `gh search code` |
| `gh_search_commits` | Search commits across GitHub | `gh search commits` |

## Tool Parameters

**Required** parameters are in bold. All tools with list/search semantics accept an optional `limit` parameter (see "Limits" section below).

### PR Tools

| Tool | Required | Optional |
|------|----------|----------|
| `gh_create_pr` | **owner, repo, title, body** | base, head, draft, labels, reviewers, assignees |
| `gh_view_pr` | **owner, repo, number** | — |
| `gh_list_prs` | **owner, repo** | state, author, label, base, head, search, limit |
| `gh_diff_pr` | **owner, repo, number** | — |
| `gh_comment_pr` | **owner, repo, number, body** | — |
| `gh_review_pr` | **owner, repo, number, event** | body |
| `gh_merge_pr` | **owner, repo, number** | method (merge/squash/rebase), delete_branch, auto |
| `gh_edit_pr` | **owner, repo, number** | title, body, base, add_labels, remove_labels, add_reviewers, remove_reviewers, add_assignees, remove_assignees |
| `gh_check_pr` | **owner, repo, number** | — |
| `gh_close_pr` | **owner, repo, number** | comment |

### Issue Tools

| Tool | Required | Optional |
|------|----------|----------|
| `gh_view_issue` | **owner, repo, number** | — |
| `gh_list_issues` | **owner, repo** | state, author, assignee, label, milestone, search, limit |
| `gh_comment_issue` | **owner, repo, number, body** | — |

### Workflow Run Tools

| Tool | Required | Optional |
|------|----------|----------|
| `gh_list_runs` | **owner, repo** | branch, status, workflow, limit |
| `gh_view_run` | **owner, repo, run_id** | log_failed |
| `gh_rerun` | **owner, repo, run_id** | failed_only |
| `gh_cancel_run` | **owner, repo, run_id** | — |

### Cache Tools

| Tool | Required | Optional |
|------|----------|----------|
| `gh_list_caches` | **owner, repo** | limit, sort, order |
| `gh_delete_cache` | **owner, repo, cache_id** | — |

### Search Tools

Search tools use `query` as the primary search string rather than `owner/repo`, since they operate across repositories.

| Tool | Required | Optional |
|------|----------|----------|
| `gh_search_prs` | **query** | repo, owner, state, author, label, limit |
| `gh_search_issues` | **query** | repo, owner, state, author, label, limit |
| `gh_search_repos` | **query** | owner, language, topic, stars, limit |
| `gh_search_code` | **query** | repo, owner, language, extension, filename, limit |
| `gh_search_commits` | **query** | repo, owner, author, limit |

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

All tools return JSON output using `gh`'s `--json` flag with a curated set of fields per tool. This keeps responses structured, predictable, and focused.

Example field sets:
- `gh_view_pr`: `number,title,body,state,author,baseRefName,headRefName,url,isDraft,mergeable,reviewDecision,statusCheckRollup,labels,assignees,createdAt,updatedAt`
- `gh_list_prs`: `number,title,state,author,headRefName,url,isDraft,createdAt,updatedAt`
- `gh_view_run`: `databaseId,name,displayTitle,status,conclusion,event,headBranch,headSha,url,createdAt,updatedAt,jobs`

For `gh_diff_pr`, the output is raw diff text (no JSON equivalent available from `gh`).

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

Tools are split into separate files by category (unlike local-git-mcp which has 5 tools in one file) since we have 24 tools.

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

    // Workflow run operations
    ListRuns(ctx context.Context, owner, repo string, opts ListRunsOpts) (string, error)
    ViewRun(ctx context.Context, owner, repo string, runID int, logFailed bool) (string, error)
    Rerun(ctx context.Context, owner, repo string, runID int, failedOnly bool) (string, error)
    CancelRun(ctx context.Context, owner, repo string, runID int) (string, error)

    // Cache operations
    ListCaches(ctx context.Context, owner, repo string, opts ListCachesOpts) (string, error)
    DeleteCache(ctx context.Context, owner, repo string, cacheID int) (string, error)

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

Each method returns `(string, error)` where the string is the raw JSON output from `gh`. The tool handlers pass this through as the MCP response — no parsing or re-serialization.

## Validation and Error Handling

**Input validation:**
- Missing required parameters return an MCP tool error: `"owner is required"`
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` pattern
- `limit` values clamped to [1, 100] range
- `event` parameter for `gh_review_pr` validated against allowed values: `approve`, `request_changes`, `comment`
- `method` parameter for `gh_merge_pr` validated against: `merge`, `squash`, `rebase`

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

| Component | Library |
|-----------|---------|
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go) |
| CLI | [cobra](https://github.com/spf13/cobra) |
| Logging | `log/slog` (stdlib) |
| Testing | [testify](https://github.com/stretchr/testify) |

## Design Decisions

**Stdio transport, not HTTP.** Same rationale as local-git-mcp: simpler, no port allocation, no TLS, no auth. The caller manages the process lifecycle.

**Explicit owner/repo, not repo_path.** Unlike local-git-mcp, most `gh` operations don't need a local clone. Passing `owner` and `repo` directly maps cleanly to `gh -R owner/repo` and lets agents operate on repos they haven't cloned.

**JSON output always.** All list/view commands use `--json` with curated field sets. Agents get structured, predictable data rather than human-readable text they'd have to parse. The one exception is `gh_diff_pr`, which returns raw diff text.

**Startup auth check.** Fail fast if `gh` isn't authenticated rather than returning auth errors on every tool call. This gives the operator immediate feedback.

**Verb-first tool naming.** `gh_create_pr` reads more naturally than `gh_pr_create`. The `gh_` prefix provides namespace safety when tools from multiple MCP servers share a flat namespace.

**No pagination.** Agents should refine queries, not paginate. The 100-item maximum (one GitHub API page) keeps responses fast and focused.

**Shell out to gh, don't use a Go GitHub client library.** The whole point is to reuse the host's existing `gh` authentication. A Go library would need its own auth configuration, defeating the purpose.

**No gh api escape hatch (initially).** Only expose focused, schema-validated tools. A raw API tool can be added later if specific use cases demand it.

## Testing

- **Unit tests** — mock `exec.Runner` in the `gh` package to verify argument construction and flag handling. Mock `Client` interface in the `tools` package to verify handler logic, parameter extraction, and error responses.
- **No integration tests initially** — would require an authenticated `gh` CLI in CI, which adds complexity. Can be added later with `//go:build integration` tag.
