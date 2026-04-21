# agent-tools

Monorepo of tools for working with AI coding agents.

## Structure

```
worktree-manager/    Git worktree manager with tmux integration — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
broker-cli/          CLI frontend for the MCP broker — see broker-cli/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
local-gh-mcp/       Stdio MCP server for GitHub operations via gh CLI — see local-gh-mcp/CLAUDE.md
agent-gateway/      Host-native HTTP/HTTPS proxy for sandboxed agents — see agent-gateway/CLAUDE.md
local-gomod-proxy/  Host-side Go module proxy for sandboxed agents — see local-gomod-proxy/CLAUDE.md
```

Each tool has its own `CLAUDE.md` with tool-specific instructions.

## Development

```bash
make install   # install all tools
make build     # build all tools
make test      # test all tools
make audit     # tidy + fmt + lint + test + govulncheck for all tools
```

Targets are forwarded to each tool's Makefile. Run from any subdirectory for a single tool.

## Service Layout

Each tool is a separate Go module under `go.work` and follows the same baseline so structure is predictable. When adding a new tool or looking for the expected shape, mirror an existing tool (e.g. `worktree-manager/`) as a template — file layout, Makefile targets, and package organization should match.

## Doc Purposes

Each doc has a distinct audience and scope — don't duplicate content between them.

- **`README.md`** — user-facing. What the tool does, how to install it, how to use it (Quick Start, Commands, security notes). Audience: anyone consuming the tool.
- **`DESIGN.md`** — source of truth for what the system should be and do. Motivation, intended behavior, architecture, key design decisions. When code and `DESIGN.md` disagree, the code is the bug. Update `DESIGN.md` deliberately when the intended design changes. Audience: anyone deciding what the tool should do.
- **`CLAUDE.md`** — conventions for Claude sessions inside the tool. Development commands, package layout, dependency flow, tool-specific gotchas (intentional `//nolint` directives, error-wrapping patterns, invariants that aren't obvious from the code). Audience: Claude and humans editing the tool.
- **`docs/*.md`** — standalone topic guides (e.g., `docs/launchd.md`). Use when a topic is too detailed for the README but isn't design-level context.
- **`examples/`** — copy-pasteable artifacts referenced from `docs/` or the README.

## Adding a New Tool

1. Create `<name>/` with `go.mod` (`module github.com/averycrespi/agent-tools/<name>`)
2. Copy `Makefile` from an existing tool and update the binary name
3. Scaffold `cmd/<binary>/main.go` + `root.go` and `internal/` packages
4. Write `README.md`, `DESIGN.md`, `CLAUDE.md` (see purposes above)
5. Add `<name>` to the `TOOLS` list in the root `Makefile`
6. Run `go mod tidy`
