# mcp-broker

MCP proxy that lets sandboxed agents use external tools without holding secrets.

## Development

```bash
make build              # go build -o mcp-broker ./cmd/mcp-broker
make install            # go install ./cmd/mcp-broker
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Integration tests use `//go:build integration`.
E2E tests use `//go:build e2e` and live in `test/e2e/`. They build and run the real binary as a subprocess.

## Architecture

Single binary, single port. `/mcp` for agents, `/dashboard/` for the web dashboard.

Pipeline: tool call → rules check → optional approval → proxy to backend → audit.

```
cmd/mcp-broker/         CLI entry point (Cobra)
internal/
  config/               JSON config with XDG paths, default backfill on load
  rules/                Glob matching (filepath.Match), first-match-wins
  audit/                SQLite (ncruces/go-sqlite3, WASM, no CGO), WAL mode
  server/               Backend interface with stdio, HTTP, SSE, and OAuth transports
  dashboard/            Embedded HTML, SSE updates, implements Approver interface
  auth/                 Bearer token auth: generation, file storage, HTTP middleware
  broker/               Orchestrator with ServerManager, AuditLogger, Approver interfaces
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Audit write errors are intentionally discarded (`_ =`) — the pipeline should not fail because audit failed
- Logger is nil-checked in packages that can be constructed without one (broker, dashboard, manager)
- `expandEnv` in server package uses `os.ExpandEnv` — supports `$VAR` and `${VAR}` anywhere in the value (e.g., `"Bearer $TOKEN"`)
- Config file permissions: `0o600` for files, `0o750` for directories
- `mcp-go` HTTP client constructor is `client.NewStreamableHttpClient` (lowercase h)
- `ncruces/go-sqlite3` requires `embed` import alongside `driver`
- OAuth config supports `"oauth": true` (all defaults) or `"oauth": {...}` (with overrides) via custom `UnmarshalJSON`
- OAuth tokens are stored in the OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
- Auth token file permissions: `0o600`, parent directories: `0o750`
- Auth token is 32 random bytes, hex-encoded (64 chars)
- Token comparison uses `crypto/subtle.ConstantTimeCompare`
- Dashboard auth uses `mcp-broker-auth` cookie (`HttpOnly`, `SameSite=Strict`)
