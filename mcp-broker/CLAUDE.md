# mcp-broker

## Development

All commands run from the `mcp-broker/` directory.

```bash
make build     # Build to ./mcp-broker
make test      # Tests with -race
make lint      # golangci-lint (must use ./... to find subdirectory packages)
make audit     # Full quality gate: tidy, fmt, lint, test, govulncheck
```

Tool dependencies (golangci-lint, goimports, govulncheck) are declared as `tool` directives in `go.mod` and invoked via `go tool <name>`.

Integration tests use `//go:build integration` and run with `go test -tags=integration ./...`.

## Architecture

Single Go binary, single port. MCP endpoint at `/mcp`, web dashboard at `/`.

Pipeline: tool call -> rules check -> optional approval -> proxy to backend -> audit.

Key packages:
- `internal/config` — JSON config with XDG paths, default backfill on load
- `internal/rules` — glob matching via `filepath.Match`, first-match-wins
- `internal/audit` — SQLite with `ncruces/go-sqlite3` (WASM, no CGO), WAL mode
- `internal/server` — `Backend` interface with stdio and HTTP implementations; tools namespaced as `<server>.<tool>`
- `internal/dashboard` — embedded HTML, SSE for real-time updates, implements `Approver` interface
- `internal/broker` — orchestrator with `ServerManager`, `AuditLogger`, `Approver` interfaces

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Audit write errors are intentionally discarded (`_ =`) — the pipeline should not fail because audit failed
- Logger is nil-checked in packages that can be constructed without one (broker, dashboard, manager)
- `expandEnv` in server package only expands full `$VAR` values, not embedded or `${VAR}` syntax
- Config file permissions: `0o600` for files, `0o750` for directories

## Dependencies

- `mcp-go` v0.45.0 — HTTP client constructor is `client.NewStreamableHttpClient` (lowercase h), returns `*client.Client`
- `ncruces/go-sqlite3` — WASM-based SQLite, requires `embed` import alongside `driver`
- `cobra` — CLI framework
- `testify` — test assertions and mocks
