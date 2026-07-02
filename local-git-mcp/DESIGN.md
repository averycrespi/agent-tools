# local-git-mcp Design

## Motivation

Sandboxed AI agents can perform most git operations locally — staging, committing, diffing, rebasing — because those don't require authentication. But operations that talk to a remote or private host credential store (clone private repos, push, pull, fetch) need credentials that the sandbox intentionally doesn't have. The agent's SSH keys and git credential helpers live on the host, not inside the sandbox.

Without a solution, agents hit a wall: they can do all the local work but can't ship it. The workaround — giving the sandbox access to credentials — defeats the purpose of sandboxing.

local-git-mcp solves this by running a minimal stdio MCP server on the host that executes authenticated git operations on behalf of the agent. It shells out to the host's `git` binary, which picks up the user's existing credential configuration (SSH keys, credential helpers, etc.).

## Architecture

local-git-mcp is a stdio MCP server. No network listener, no config file, no state. A caller spawns it as a subprocess and communicates over stdin/stdout using the MCP protocol.

The caller must provide one or more allowed host path prefixes at startup, for example `local-git-mcp /shared/worktrees /other/repo/root`. Tool calls can access only repositories or clone destinations at those prefixes or their descendants. For the old unrestricted behavior, the caller must explicitly pass `--allow-all-paths`.

Every git subprocess receives the MCP request context plus a per-command timeout. The default timeout is 5 minutes and can be changed with `--git-timeout`; `--git-timeout 0` disables the timeout.

## Tools

Six tools. Existing repo-scoped tools require a `repo_path` parameter that is validated to be an existing git repository. `clone_github_repo` instead takes a GitHub repository slug and an allowed destination parent because the target repo does not exist yet.

| Tool                | Description                           | Parameters                                                                                               | Annotation   |
| ------------------- | ------------------------------------- | -------------------------------------------------------------------------------------------------------- | ------------ |
| `push`              | Push commits to remote                | `repo_path`, `remote` (default: origin), `refspec` (optional), `force` (bool, uses `--force-with-lease`) | destructive  |
| `pull`              | Pull from remote                      | `repo_path`, `remote` (default: origin), `branch` (optional), `rebase` (bool, default: false)            | additive     |
| `fetch`             | Fetch from remote without merging     | `repo_path`, `remote` (default: origin), `refspec` (optional)                                            | idempotent   |
| `clone_github_repo` | Clone a GitHub repo over SSH          | `repository`, `destination_dir`                                                                          | additive     |
| `list_remote_refs`  | List refs (branches/tags) on a remote | `repo_path`, `remote` (default: origin)                                                                  | read         |
| `list_remotes`      | Show configured remotes and URLs      | `repo_path`                                                                                              | read (local) |

### Annotations

Each tool declares MCP `ToolAnnotation` hints so callers can reason about safety without parsing descriptions. Five presets are defined in `internal/tools/tools.go`:

- **`annRead`** — `ReadOnlyHint=true`, `OpenWorldHint=true`. Read tools that talk to a remote.
- **`annReadLocal`** — `ReadOnlyHint=true`, `OpenWorldHint=false`. Read tools that only touch local repo state.
- **`annIdempotent`** — `IdempotentHint=true`, `DestructiveHint=false`, `OpenWorldHint=true`. Repeat calls with the same args converge to the same local state.
- **`annAdditive`** — `DestructiveHint=false`, `OpenWorldHint=true`. Mutates state, not destructive.
- **`annDestructive`** — `DestructiveHint=true`, `OpenWorldHint=true`. Rewrites or removes state non-trivially.

`push` is annotated conservatively as destructive even without `force=true`, because the underlying capability can rewrite remote history.

### Parameter details

- **`repo_path`** (required for repo-scoped tools) — absolute path to a git repository on the host. Must be absolute (relative paths are rejected) and must be equal to or inside one of the allowed path prefixes supplied at startup. Validated before every repo-scoped operation: must be allowed, must exist, and must contain a git repo (`git rev-parse --git-dir`).
- **`repository`** (required for `clone_github_repo`) — GitHub repository slug in `owner/repo` form. Full URLs, HTTPS URLs, SSH URLs, path traversal, extra path components, whitespace, and malformed values are rejected. The tool derives the SSH URL as `git@github.com:owner/repo.git`.
- **`destination_dir`** (required for `clone_github_repo`) — absolute existing parent directory on the host. Must be equal to or inside one of the allowed path prefixes, must not be a symlink, and must be a directory. The target path is derived as `<destination_dir>/<repo>` and the call fails if that path already exists.
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

Startup validates access policy before serving MCP:

1. At least one allowed path prefix is required unless `--allow-all-paths` is provided.
2. Allowed path prefixes must be absolute paths.
3. `--allow-all-paths` cannot be combined with explicit allowed path prefixes.

Repo-scoped tool calls validate `repo_path` before executing:

1. **Absolute path** — `repo_path` must be an absolute path. Relative paths are rejected.
2. **Allowed path** — `repo_path` must equal or descend from an allowed prefix. Sibling prefixes are not accepted: `/repo2` is outside allowed prefix `/repo`.
3. **Path exists** — directory must be present on the host.
4. **Is a git repo** — `git -C <path> rev-parse --git-dir` must succeed.

`clone_github_repo` validates clone-specific inputs before executing:

1. **GitHub slug** — `repository` must be a strict `owner/repo` slug, not a URL or arbitrary remote.
2. **Absolute destination** — `destination_dir` must be an absolute path.
3. **Allowed destination** — `destination_dir` must equal or descend from an allowed prefix.
4. **Existing non-symlink directory** — `destination_dir` must exist, must be a directory, and must not be a symlink.
5. **Nonexistent target** — `<destination_dir>/<repo>` must not exist.
6. **Post-clone repo validation** — after `git clone -- git@github.com:owner/repo.git <target>`, `git -C <target> rev-parse --git-dir` must succeed before the tool returns success.

Errors are returned as MCP tool error responses. Git's stderr is included in the error message so agents get actionable feedback (e.g., "remote not found", "permission denied").

No retries or special error recovery — git's exit code and output are passed through faithfully. If a command exceeds its timeout or the MCP request context is canceled, the command is canceled and the tool returns the context error.

## Security

local-git-mcp restricts repository operations and clone destinations to explicit host path prefixes supplied at startup. This prevents sandboxed callers from asking the host-side server to operate on unrelated repositories or clone into unrelated directories that are not shared with the sandbox or should not be exposed.

Callers that intentionally want unrestricted host-path access must pass `--allow-all-paths`.

## Tech stack

| Component    | Library                                        |
| ------------ | ---------------------------------------------- |
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go)  |
| CLI          | [cobra](https://github.com/spf13/cobra)        |
| Logging      | `log/slog` (stdlib)                            |
| Testing      | [testify](https://github.com/stretchr/testify) |

## Design decisions

**Stdio transport, not HTTP.** Stdio is simpler — no port allocation, no TLS, no auth. The caller manages the process lifecycle.

**No config file.** Access policy and command timeout are supplied as startup arguments rather than a config file. Credentials come from the host's existing git setup.

**Explicit path allowlist.** The server rejects `repo_path` values and clone `destination_dir` values outside configured allowed prefixes before running git. This matches sandbox/host shared-path setups and avoids exposing every host repository or writable host directory by default.

**GitHub clone is intentionally narrow.** `clone_github_repo` accepts only `owner/repo` and always derives `git@github.com:owner/repo.git`. This keeps v1 from becoming a generic remote clone primitive and prevents callers from smuggling arbitrary hosts or URL schemes through the clone API.

**`--force-with-lease`, never `--force`.** Force pushing is useful for agents (rebased branches), but bare `--force` risks destroying others' work. `--force-with-lease` provides the same functionality with a safety check.

**Shell out to git, don't use a library.** The whole point is to use the host's git binary with its configured credential helpers, SSH keys, and settings. A Go git library would need its own credential configuration.

## Testing

- **Unit tests** — mock `exec.Runner` to verify argument construction, validation logic, and error handling without running real git commands.
- **Integration tests** (`-tags=integration`) — create temporary git repos with local file:// remotes, run real git operations, verify outputs.
