# local-gh-mcp

Stdio MCP server for GitHub operations via the gh CLI.

## Development

```bash
make build              # go build -o local-gh-mcp ./cmd/local-gh-mcp
make install            # go install ./cmd/local-gh-mcp
make test               # go test -race ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

Stdio MCP server. No config, no state, no network listener.

Shells out to the host's `gh` binary for all operations. Validates `gh auth status` on startup — exits immediately if not authenticated.

```
cmd/local-gh-mcp/       CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  gh/                    GitHub operations via exec.Runner
  tools/
    tools.go             Tool registration and dispatch
    pr.go                PR tool definitions and handlers
    issue.go             Issue tool definitions and handlers
    run.go               Workflow run tool definitions and handlers
    cache.go             Cache tool definitions and handlers
    search.go            Search tool definitions and handlers
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- gosec nolint directives on os/exec are intentional for CLI
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` pattern before use
- Repo targeting: `-R owner/repo` flag (not repo_path)
- JSON output: all list/view tools use `--json` with curated field sets
- Limits: default 30, max 100, clamped silently (derived from gh CLI defaults and GitHub API page size)
- Stdio MCP server setup: `mcpserver.NewMCPServer()` + `srv.AddTool(tool, handler.Handle)` + `mcpserver.ServeStdio(srv)`
- `mcp-go` v0.45.0: use `req.GetArguments()` helper instead of `req.Params.Arguments`
