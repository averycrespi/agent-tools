# local-git-mcp

A stdio MCP server that executes authenticated git remote operations on behalf of sandboxed agents.

Sandboxed agents can do most git operations locally — staging, committing, diffing, rebasing — because those don't need authentication. But cloning private repositories and pushing, pulling, or fetching require credentials that the sandbox intentionally doesn't have. local-git-mcp runs on the host where SSH keys and credential helpers are available, and exposes these operations over MCP.

## How it works

```
Agent (in sandbox)                    Host
─────────────────                    ─────
git add, commit,     ──MCP──▶    local-git-mcp
diff, rebase, ...                    │
(no auth needed)                 git clone, push,
                                 pull, fetch
                                 (uses host credentials)
```

local-git-mcp is a stdio MCP server — a caller spawns it as a subprocess and communicates over stdin/stdout. It shells out to the host's `git` binary, which picks up the user's existing credential configuration.

At startup, callers must provide one or more existing allowed path prefixes. Symlinks in those prefixes are resolved before serving requests. Tool calls can only access repositories or clone destinations at the resolved paths or their descendants. Starting without allowed paths fails unless `--allow-all-paths` is provided.

## Tools

| Tool                | Description                                              |
| ------------------- | -------------------------------------------------------- |
| `push`              | Push commits to a remote (supports `--force-with-lease`) |
| `pull`              | Pull from a remote (supports `--rebase`)                 |
| `fetch`             | Fetch from a remote without merging                      |
| `clone_github_repo` | Clone a GitHub repository over SSH                       |
| `list_remote_refs`  | List refs (branches, tags) on a remote                   |
| `list_remotes`      | List configured remotes and their URLs                   |

Repo-scoped tools require a `repo_path` parameter — an absolute path to a git repository on the host. The path must resolve to the same location as, or a descendant of, one of the allowed path prefixes provided when the server starts.

`clone_github_repo` requires `repository` in `owner/repo` form and `destination_dir`, an absolute existing parent directory inside the allowed path prefixes. It resolves symlinks before checking containment, rejects a destination that is itself a symlink, always clones over SSH as `git@github.com:owner/repo.git`, derives the target path as `<resolved-destination>/<repo>`, fails if that path already exists, and returns JSON text containing the cloned `repo_path`.

Tools that accept `remote` require a configured remote name such as `origin`; raw transport URLs such as `https://...`, `ssh://...`, and `file://...` are rejected.

### Remote-operation contract

`push` requires `repo_path`, `remote`, `remote_url`, `source_ref`, `destination_ref`, and an explicit `force` boolean. Both refs must be nonempty, fully qualified refs under `refs/heads/` or `refs/tags/`; the server validates them and constructs the single `<source_ref>:<destination_ref>` refspec. `force: false` performs an ordinary push, while `force: true` uses exactly `--force-with-lease`. Raw push `refspec` values and defaults for `remote` or `force` are not supported.

`fetch` and `pull` require `repo_path`, `remote`, and `remote_url`. Fetch keeps its optional `refspec`; pull keeps its optional `branch` and `rebase` fields. These operations do not default `remote`. `list_remote_refs` still defaults `remote` to `origin`.

`remote_url` is a policy-visible assertion, not the transport operand. For push, the server resolves all effective push URLs and requires exactly one exact match. For fetch and pull, it compares the first effective fetch URL. Comparisons are byte-for-byte with no URL normalization, and successful commands still execute through the configured remote name. Use `list_remotes` or the corresponding `git remote get-url` command to obtain the operation-specific value.

This is an intentional breaking API change. All six tools reject unknown top-level arguments, missing required fields, and wrongly typed fields before invoking their handlers.

`remote_url` is visible to mcp-broker policy and audit processing. Never submit an HTTP(S) URL containing URI userinfo, including username-only or username/password forms; use SSH or an external credential helper instead. local-git-mcp rejects credential-bearing supplied and resolved HTTP(S) URLs without echoing them in tool errors.

Destination verification supports cooperative policy mediation: it catches mistakes and links broker-visible arguments to Git's ordinary effective configuration. It does not pin the transport or contain hostile Git configuration, URL rewrites, remote helpers, redirects, or configuration changes after verification.

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
make test-integration   # Tagged integration cases, without ordinary reruns
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
