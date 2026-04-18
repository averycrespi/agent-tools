# local-gomod-proxy

HTTP Go module proxy that lets sandboxed agents resolve private Go dependencies using the host's git credentials.

## Development

```bash
make build              # go build -o local-gomod-proxy ./cmd/local-gomod-proxy
make install            # go install ./cmd/local-gomod-proxy
make test               # go test -race ./...
make test-integration   # go test -race -tags=integration ./...
make test-e2e           # go test -race -tags=e2e -timeout=60s ./test/e2e/...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing. Integration tests use `//go:build integration`. E2E tests use `//go:build e2e`.

## Architecture

Single HTTP binary. No config file, no persistent state.

Private module requests shell out to `go mod download -json` on the host and stream the resulting files from `GOMODCACHE`. Public module requests are reverse-proxied to `proxy.golang.org`.

```
cmd/local-gomod-proxy/  CLI entry point (Cobra) — serve subcommand
internal/
  exec/                  Runner interface for command execution
  goenv/                 Reads GOPRIVATE / GOMODCACHE / GOVERSION via `go env -json`
  router/                GOPRIVATE glob matching — selects private or public fetcher
  private/               PrivateFetcher — shells out to `go mod download`, streams files
  public/                PublicFetcher — reverse-proxy to proxy.golang.org
  server/                HTTP handler wiring router + fetchers
test/e2e/               End-to-end tests
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Command output interpolated into errors with `%s` after trimming (never `%w` for command stderr)
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have unit tests
- gosec `nolint` directives on `os/exec` calls are acceptable inside the `exec` package only; also acceptable inside `private.streamFile` on `os.Open`
- `--private` flag overrides `go env GOPRIVATE`; if neither is set, startup fails with an actionable error
- `GOPRIVATE` and `GOMODCACHE` are read via `go env -json`, not `os.Getenv` — users commonly set these via `go env -w`
- No application-level auth — the proxy relies on binding to a local-only interface. See DESIGN.md for rationale (Go ≥ 1.22 HTTPS gate on every supported auth mechanism)
