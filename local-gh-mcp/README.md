# local-gh-mcp

A stdio MCP server that executes GitHub operations on behalf of sandboxed agents via the `gh` CLI.

Sandboxed agents need to interact with GitHub — creating PRs, checking CI status, reading issues, debugging workflow failures — but giving them credentials defeats the purpose of sandboxing. The official GitHub MCP server requires OAuth (which needs GitHub App installation) or personal access tokens (another secret to manage). Meanwhile, the host machine already has `gh` CLI authenticated and working. local-gh-mcp reuses this existing authentication by running a stdio MCP server on the host that shells out to `gh`.

## How it works

```
Agent (in sandbox)                    Host
─────────────────                    ─────
needs to create PR,  ──MCP──>    local-gh-mcp
check CI, read issues               │
(no credentials)                 gh pr, issue, run, ...
                                 (uses host's gh auth)
                                     │
                                 GitHub API
```

local-gh-mcp is a stdio MCP server — a caller spawns it as a subprocess and communicates over stdin/stdout. It shells out to the host's `gh` binary, which picks up the user's existing authentication.

## Prerequisites

- **Go 1.25+**
- **gh CLI** installed and authenticated (`gh auth login`)

The server validates `gh auth status` on startup and exits immediately if not authenticated.

## Tools

### PR Tools (11)

| Tool | Description |
|------|-------------|
| `gh_create_pr` | Create a pull request |
| `gh_view_pr` | View PR metadata and description as structured markdown |
| `gh_list_prs` | List PRs as markdown bullets |
| `gh_diff_pr` | View diff with file summary table |
| `gh_comment_pr` | Add a comment to a PR |
| `gh_review_pr` | Submit a review (approve, request changes, or comment) |
| `gh_merge_pr` | Merge a PR |
| `gh_edit_pr` | Edit PR metadata |
| `gh_check_pr` | View CI/status checks as markdown bullet list |
| `gh_close_pr` | Close a PR |
| `gh_list_pr_comments` | List PR comments as markdown |

### Issue Tools (4)

| Tool | Description |
|------|-------------|
| `gh_view_issue` | View issue metadata and description as structured markdown |
| `gh_list_issues` | List issues as markdown bullets |
| `gh_comment_issue` | Add a comment to an issue |
| `gh_list_issue_comments` | List issue comments as markdown |

### Workflow Run Tools (4)

| Tool | Description |
|------|-------------|
| `gh_list_runs` | List workflow runs as markdown bullets |
| `gh_view_run` | View run details as structured markdown (or raw logs with log_failed) |
| `gh_rerun` | Rerun a failed or specific workflow run |
| `gh_cancel_run` | Cancel an in-progress workflow run |

### Cache Tools (2)

| Tool | Description |
|------|-------------|
| `gh_list_caches` | List GitHub Actions caches |
| `gh_delete_cache` | Delete a cache entry |

### Search Tools (5)

| Tool | Description |
|------|-------------|
| `gh_search_prs` | Search pull requests, returns markdown bullets |
| `gh_search_issues` | Search issues, returns markdown bullets |
| `gh_search_repos` | Search repositories, returns markdown bullets |
| `gh_search_code` | Search code, returns markdown bullets |
| `gh_search_commits` | Search commits, returns markdown bullets |

All tools targeting a specific repository use `owner` and `repo` parameters (mapped to `gh -R owner/repo`). Search tools use a `query` parameter instead, since they operate across repositories. List/search tools accept an optional `limit` (default 30, max 100). View and comment tools accept an optional `max_body_length` (default 2000, max 50000) to control text truncation.

All read tools return **structured markdown** (not raw JSON) — author objects are flattened to `@login`, long bodies are truncated, and output is formatted with headers and labeled fields for easy LLM consumption. Write tools return plain text confirmations.

## Quick start

```bash
# Build
make build

# Use as a stdio MCP backend (e.g., in mcp-broker config)
{
  "servers": {
    "local-gh": {
      "command": "local-gh-mcp"
    }
  }
}
```

## Development

```bash
make build              # Build binary to ./local-gh-mcp
make test               # Run tests with race detector
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Requires Go 1.25+. Tool dependencies (golangci-lint, goimports, govulncheck) are managed via `go tool` directives in `go.mod`.

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.

```
cmd/local-gh-mcp/       CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  format/                Markdown formatting for tool output
  gh/                    GitHub operations via exec.Runner
  tools/
    tools.go             Tool registration and dispatch
    pr.go                PR tool definitions and handlers
    issue.go             Issue tool definitions and handlers
    run.go               Workflow run tool definitions and handlers
    cache.go             Cache tool definitions and handlers
    search.go            Search tool definitions and handlers
```
