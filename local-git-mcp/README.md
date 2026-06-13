# local-git-mcp

A stdio MCP server that executes authenticated git remote operations on behalf of sandboxed agents.

Sandboxed agents can do most git operations locally — staging, committing, diffing, rebasing — because those don't need authentication. But pushing, pulling, and fetching require credentials that the sandbox intentionally doesn't have. local-git-mcp runs on the host where SSH keys and credential helpers are available, and exposes these operations over MCP.

## How it works

```
Agent (in sandbox)                    Host
─────────────────                    ─────
git add, commit,     ──MCP──▶    local-git-mcp
diff, rebase, ...                    │
(no auth needed)                 git push, pull, fetch
                                 (uses host credentials)
```

local-git-mcp is a stdio MCP server — a caller spawns it as a subprocess and communicates over stdin/stdout. It shells out to the host's `git` binary, which picks up the user's existing credential configuration.

At startup, callers must provide one or more allowed path prefixes. Tool calls can only access repositories at those paths or their descendants. Starting without allowed paths fails unless `--allow-all-paths` is provided.

## Tools

| Tool               | Description                                              |
| ------------------ | -------------------------------------------------------- |
| `push`             | Push commits to a remote (supports `--force-with-lease`) |
| `pull`             | Pull from a remote (supports `--rebase`)                 |
| `fetch`            | Fetch from a remote without merging                      |
| `list_remote_refs` | List refs (branches, tags) on a remote                   |
| `list_remotes`     | List configured remotes and their URLs                   |

All tools require a `repo_path` parameter — an absolute path to a git repository on the host. The path must be equal to or inside one of the allowed path prefixes provided when the server starts.

## Quick start

```bash
# Build
make build

# Use as a stdio MCP backend (e.g., in mcp-broker config)
{
  "servers": {
    "local-git": {
      "command": "local-git-mcp",
      "args": ["/shared/worktrees", "/other/repo/root"]
    }
  }
}

# Explicitly allow all host paths, preserving the old unrestricted behavior
local-git-mcp --allow-all-paths

# Override the per-git-command timeout (default: 5m; 0 disables it)
local-git-mcp --git-timeout 20m /shared/worktrees
```

`--allow-all-paths` disables repository path isolation. Use it only when the caller is trusted to operate on any absolute git repository path visible to the host; the server logs a startup warning when this flag is enabled.

Each git command runs with a finite timeout so hung remote operations do not block the MCP handler forever. The default is 5 minutes per git command. Use `--git-timeout` to adjust it, or `--git-timeout 0` to disable the timeout.

## Development

```bash
make build              # Build binary to ./local-git-mcp
make test               # Run tests with race detector
make test-integration   # Run integration tests (-tags=integration)
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Requires Go 1.25+. Tool dependencies (golangci-lint, goimports, govulncheck) are managed via `go tool` directives in `go.mod`.

## Architecture

See [DESIGN.md](DESIGN.md) for the full design document.

```
cmd/local-git-mcp/      CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  git/                   Git remote operations via exec.Runner
  tools/                 MCP tool definitions and handlers
```
