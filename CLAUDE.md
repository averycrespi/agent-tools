# agent-tools

Monorepo of CLI tools for working with AI coding agents.

## Structure

```
worktree-manager/    Go CLI tool "wt" — see worktree-manager/CLAUDE.md
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Go CLI tool "sb" — see sandbox-manager/CLAUDE.md
```

Each tool has its own `CLAUDE.md` with tool-specific instructions.

## Conventions

- Each tool is a separate Go module under `go.work`
