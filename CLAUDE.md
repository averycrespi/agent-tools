# agent-tools

Monorepo of tools for working with AI coding agents.

## Structure

```
worktree-manager/    Git worktree manager with tmux integration — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
pi-dispatcher/       Background Pi coding-agent dispatcher — see pi-dispatcher/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
local-gomod-proxy/  Host-side Go module proxy for sandboxed agents — see local-gomod-proxy/CLAUDE.md
hindsight/          Auxiliary Docker Compose memory stack — see hindsight/README.md
```

Each Go tool has its own `CLAUDE.md` with tool-specific instructions. `hindsight/` is an auxiliary Docker Compose stack, not a Go tool.

## Development

```bash
npm install    # install Husky/Prettier deps and Git hooks
make install   # install all Go tool binaries
make build     # build all Go tools
make test      # test all Go tools
make audit     # tidy + fmt + lint + test + govulncheck for all Go tools
```

Targets are forwarded to each tool's Makefile. Run from any subdirectory for a single tool. The pre-commit hook runs `lint-staged` for Go and docs formatting, then `make lint` for Go linting.

## Service Layout

Each Go tool is a separate Go module under `go.work` and follows the same baseline so structure is predictable. When adding a new Go tool or looking for the expected shape, mirror an existing tool (e.g. `worktree-manager/`) as a template — file layout, Makefile targets, and package organization should match. Auxiliary non-Go stacks such as `hindsight/` are documented separately and are not forwarded by the root Makefile.

## Go Conventions

Keep each tool as an independent Go module. Prefer copying small local interfaces and helpers, such as `exec.Runner`, over introducing a shared internal module. When adding subprocess execution, use a context-aware runner with finite deadlines; copy the nearest tool's established pattern and keep timeout behavior documented in that tool's `DESIGN.md` or `CLAUDE.md`.

## CLI Conventions

For Cobra commands, avoid exposing generic argument validation errors such as `accepts 2 arg(s), received 0`.
Prefer command-specific `Args` functions that name missing arguments and include the command usage or a short example when quoting/order matters.
Let Cobra print execution errors once; don't wrap `Execute()` with a second stderr print unless `SilenceErrors` is enabled.

## Doc Purposes

Each doc has a distinct audience and scope — don't duplicate content between them.

- **`README.md`** — user-facing. What the tool does, how to install it, how to use it (Quick Start, Commands, security notes). Audience: anyone consuming the tool.
- **`DESIGN.md`** — source of truth for what the system should be and do. Motivation, intended behavior, architecture, key design decisions. When code and `DESIGN.md` disagree, the code is the bug. Update `DESIGN.md` deliberately when the intended design changes. Audience: anyone deciding what the tool should do.
- **`CLAUDE.md`** — conventions for Claude sessions inside the tool. Development commands, package layout, dependency flow, tool-specific gotchas (intentional `//nolint` directives, error-wrapping patterns, invariants that aren't obvious from the code). Audience: Claude and humans editing the tool. Each `CLAUDE.md` has a sibling `AGENTS.md` symlink pointing to it, so non-Claude agents that look for `AGENTS.md` get the same content.
- **`docs/*.md`** — standalone topic guides (e.g., `docs/launchd.md`). Use when a topic is too detailed for the README but isn't design-level context.
- **`examples/`** — copy-pasteable artifacts referenced from `docs/` or the README.

## Adding a New Go Tool

1. Create `<name>/` with `go.mod` (`module github.com/averycrespi/agent-tools/<name>`)
2. Copy `Makefile` and `.golangci.yml` from an existing tool and update the binary name
3. Scaffold `cmd/<binary>/main.go` + `root.go` and `internal/` packages
4. Write `README.md`, `DESIGN.md`, `CLAUDE.md` (see purposes above), and add an `AGENTS.md` symlink to `CLAUDE.md` (`ln -s CLAUDE.md AGENTS.md`)
5. Add `<name>` to the `TOOLS` list in the root `Makefile`
6. Run `go mod tidy`
