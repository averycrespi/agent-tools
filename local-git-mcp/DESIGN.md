# local-git-mcp Design

## Motivation

Sandboxed AI agents can perform most git operations locally — staging, committing, diffing, rebasing — because those don't require authentication. But operations that talk to a remote (push, pull, fetch) need credentials that the sandbox intentionally doesn't have. The agent's SSH keys and git credential helpers live on the host, not inside the sandbox.

Without a solution, agents hit a wall: they can do all the local work but can't ship it. The workaround — giving the sandbox access to credentials — defeats the purpose of sandboxing.

local-git-mcp solves this by running a minimal stdio MCP server on the host that executes authenticated git operations on behalf of the agent. It shells out to the host's `git` binary, which picks up the user's existing credential configuration (SSH keys, credential helpers, etc.).

## Architecture

local-git-mcp is a stdio MCP server. No network listener, no config file, no state. A caller spawns it as a subprocess and communicates over stdin/stdout using the MCP protocol.

## Tools

Five tools, all requiring a `repo_path` parameter that is validated to be an existing git repository:

| Tool | Description | Parameters |
|------|-------------|------------|
| `git_push` | Push commits to remote | `repo_path`, `remote` (default: origin), `refspec` (optional), `force` (bool, uses `--force-with-lease`) |
| `git_pull` | Pull from remote | `repo_path`, `remote` (default: origin), `branch` (optional), `rebase` (bool, default: false) |
| `git_fetch` | Fetch from remote without merging | `repo_path`, `remote` (default: origin), `refspec` (optional) |
| `git_list_remote_refs` | List refs (branches/tags) on a remote | `repo_path`, `remote` (default: origin) |
| `git_list_remotes` | Show configured remotes and URLs | `repo_path` |

### Parameter details

- **`repo_path`** (required, all tools) — absolute path to a git repository on the host. Must be absolute (relative paths are rejected). Validated before every operation: must exist and contain a git repo (`git rev-parse --git-dir`).
- **`remote`** (optional, default: "origin") — the remote name to operate on.
- **`refspec`** (optional) — git refspec for push/fetch (e.g., `refs/heads/main`).
- **`branch`** (optional) — branch name for pull.
- **`force`** (optional, push only) — when true, uses `--force-with-lease` (never bare `--force`).
- **`rebase`** (optional, pull only) — when true, uses `--rebase`.

## Project structure

```
local-git-mcp/
├── cmd/
│   └── local-git-mcp/
│       ├── main.go              # Entry point
│       └── root.go              # Cobra root cmd, MCP server setup
├── internal/
│   ├── exec/
│   │   ├── runner.go            # Runner interface (same as other tools)
│   │   └── runner_test.go
│   ├── git/
│   │   ├── git.go               # Git operations via exec.Runner
│   │   └── git_test.go
│   └── tools/
│       ├── tools.go             # MCP tool definitions + handlers
│       └── tools_test.go
├── go.mod
├── Makefile
├── CLAUDE.md
├── DESIGN.md
└── README.md
```

## Validation and error handling

Every tool call validates `repo_path` before executing:

1. **Absolute path** — `repo_path` must be an absolute path. Relative paths are rejected.
2. **Path exists** — directory must be present on the host.
3. **Is a git repo** — `git -C <path> rev-parse --git-dir` must succeed.

Errors are returned as MCP tool error responses. Git's stderr is included in the error message so agents get actionable feedback (e.g., "remote not found", "permission denied").

No retries or special error recovery — git's exit code and output are passed through faithfully.

## Security

local-git-mcp has no access control of its own. It trusts its caller to handle authorization.

## Tech stack

| Component | Library |
|-----------|---------|
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go) |
| CLI | [cobra](https://github.com/spf13/cobra) |
| Logging | `log/slog` (stdlib) |
| Testing | [testify](https://github.com/stretchr/testify) |

## Design decisions

**Stdio transport, not HTTP.** Stdio is simpler — no port allocation, no TLS, no auth. The caller manages the process lifecycle.

**No config file.** There's nothing to configure. The server executes git commands in whatever directory the caller specifies. Credentials come from the host's existing git setup.

**No access control.** Authorization is the caller's responsibility. Adding access control here would duplicate policy logic that belongs elsewhere.

**`--force-with-lease`, never `--force`.** Force pushing is useful for agents (rebased branches), but bare `--force` risks destroying others' work. `--force-with-lease` provides the same functionality with a safety check.

**Shell out to git, don't use a library.** The whole point is to use the host's git binary with its configured credential helpers, SSH keys, and settings. A Go git library would need its own credential configuration.

## Testing

- **Unit tests** — mock `exec.Runner` to verify argument construction, validation logic, and error handling without running real git commands.
- **Integration tests** (`-tags=integration`) — create temporary git repos with local file:// remotes, run real git operations, verify outputs.
