# local-git-mcp

Stdio MCP server for authenticated git remote operations.

## Development

```bash
make build              # go build -o local-git-mcp ./cmd/local-git-mcp
make install            # go install ./cmd/local-git-mcp
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Integration tests use `//go:build integration`.

## Architecture

Stdio MCP server. No config, no state, no network listener.

Shells out to the host's `git` binary for all operations.

```
cmd/local-git-mcp/      CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  git/                   Git remote operations via exec.Runner
  tools/                 MCP tool definitions and handlers
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- gosec nolint directives on os/exec are intentional for CLI
- Always use `--` end-of-options separator before user-controlled positional args in git commands to prevent argument injection
- Stdio MCP server setup: `mcpserver.NewMCPServer()` + `srv.AddTool(tool, handler.Handle)` + `mcpserver.ServeStdio(srv)` — handler signature is `func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error)`
- `mcp-go` v0.45.0: use `req.GetArguments()` helper instead of `req.Params.Arguments` (typed as `any`, not `map[string]any`)
