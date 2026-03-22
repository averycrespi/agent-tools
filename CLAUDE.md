# agent-tools

Monorepo of tools for working with AI coding agents.

## Structure

```
worktree-manager/    Git worktree manager with tmux integration — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
local-gh-mcp/       Stdio MCP server for GitHub operations via gh CLI — see local-gh-mcp/CLAUDE.md
```

Each tool has its own `CLAUDE.md` with tool-specific instructions.

## Conventions

- Each tool is a separate Go module under `go.work`
