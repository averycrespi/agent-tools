# agent-tools

Monorepo of tools for working with AI coding agents.

## Structure

```
worktree-manager/    Git worktree manager with tmux integration — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
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

## Conventions

- Each tool is a separate Go module under `go.work`
