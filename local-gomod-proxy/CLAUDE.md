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
  state/                 State dir, cert load-or-generate, credentials load-or-generate
  auth/                  HTTP Basic auth middleware
test/e2e/               End-to-end tests
```

## Documentation

Keep all docs in sync when changing behavior, flags, layout, or deployment. The full set:

- `CLAUDE.md` — this file. Conventions, architecture summary, and doc-sync rules.
- `DESIGN.md` — motivation, request flow, protocol endpoints, and design decisions.
- `README.md` — user-facing install, run, sandbox integration, and security notes.
- `docs/*.md` — topic-specific guides (e.g. `docs/launchd.md`).
- `examples/**` — example configs referenced from the docs above (e.g. `examples/launchd/*.plist`).

When you change a flag, endpoint, env-var contract, or file layout, audit every doc listed above and update the ones that reference it. Don't leave a stale flag name in README and a fresh one in DESIGN.md.

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- Command output interpolated into errors with `%s` after trimming (never `%w` for command stderr)
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have unit tests
- gosec `nolint` directives on `os/exec` calls are acceptable inside the `exec` package only; also acceptable inside `private.streamFile` on `os.Open`
- `--private` flag overrides `go env GOPRIVATE`; if neither is set, startup fails with an actionable error
- `GOPRIVATE` and `GOMODCACHE` are read via `go env -json`, not `os.Getenv` — users commonly set these via `go env -w`
- Auth: HTTPS + HTTP basic auth enforced on every request. Cert, key, and credentials live under `$XDG_STATE_HOME/local-gomod-proxy/` (fallback `~/.local/state/local-gomod-proxy/`). Loopback binding is still enforced — TLS + auth layer on top, they do not replace the network boundary.
