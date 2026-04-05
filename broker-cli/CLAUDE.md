# broker-cli

CLI frontend for the MCP broker. Discovers tools at runtime and exposes them as subcommands.

## Development

```bash
make build              # go build -o broker-cli ./cmd/broker-cli
make install            # go install ./cmd/broker-cli
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

CLI-only binary. Connects to the MCP broker's HTTP endpoint, discovers tools via `tools/list`, and builds a cobra command tree before `Execute()` runs.

```
cmd/broker-cli/      CLI entry point (main.go + root.go)
internal/
  client/            MCP HTTP client with bearer token auth
  cache/             File-based tool list cache (30s TTL, keyed by endpoint hash)
  flags/             JSON Schema → cobra flags mapper
  output/            MCP content blocks → JSON array formatter
  tree/              Dynamic cobra command tree builder
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All errors printed to stderr as JSON: `{"error": "..."}`
- Output always printed to stdout as a JSON array
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- `internal/client/` tests are integration-tagged (`//go:build integration`)
- Tool discovery is cached in `$TMPDIR/broker-cli-tools-<hash>.json` (30s TTL)
- Tool names: dots map to command hierarchy, underscores normalize to hyphens
- Approval wait: broker holds the HTTP connection; CLI prints "waiting for approval..." to stderr after 1s via goroutine
- `mcp-go` v0.45.0: `client.NewStreamableHttpClient` + `transport.WithHTTPHeaders` for auth
