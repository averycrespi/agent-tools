# local-gomod-proxy

A host-side Go module proxy that lets sandboxed agents resolve private Go dependencies without holding the host's git credentials. Public modules are forwarded to `proxy.golang.org`; private modules (matched by `GOPRIVATE` patterns) are fetched via the host's git credentials and served back to the sandbox. The sandbox holds only a short-lived bearer token.

## Install

```bash
# From the local-gomod-proxy subdirectory:
make install

# Or from the repo root:
make install
```

Requires Go 1.21+.

## Run

```bash
local-gomod-proxy serve [--addr :7070] [--private PATTERN] [--upstream URL]
```

| Flag         | Default                      | Description                                                                      |
| ------------ | ---------------------------- | -------------------------------------------------------------------------------- |
| `--addr`     | `:7070`                      | Address to listen on                                                             |
| `--private`  | _(reads `go env GOPRIVATE`)_ | GOPRIVATE-style glob patterns for private modules. Overrides `go env GOPRIVATE`. |
| `--upstream` | `https://proxy.golang.org`   | Public upstream proxy URL                                                        |

`GOPRIVATE` must be set — either via `go env -w GOPRIVATE=github.com/your-org/*` on the host or via `--private`. If neither is set, startup fails with an actionable error.

### Token subcommand

```bash
local-gomod-proxy token
```

Prints the current auth token (64-character hex string) to stdout. Use this to construct the `GOPROXY` URL in sandbox provisioning scripts.

## How the sandbox consumes it

The intended configuration inside the sandbox is:

```sh
export GOPROXY=http://_:$(local-gomod-proxy token)@host.lima.internal:7070/
export GOSUMDB=off
# Do NOT set GOPRIVATE inside the sandbox — that would bypass the proxy.
```

`GOSUMDB=off` is acceptable in the sandbox because `go.sum` (committed to the repo) is the primary integrity check.

> **Known limitation: URL-embedded Basic Auth over plain HTTP is refused by Go >= 1.22.**
>
> Go's module tooling (`cmd/go/internal/web/http.go:244`) refuses to send credentials embedded in a `http://user:pass@...` URL over plain (non-TLS) HTTP. This has been the case since Go 1.22 (see [Go issue #42135](https://github.com/golang/go/issues/42135)).
>
> **The `GOPROXY` form above will not work out of the box** when the sandbox's `go` binary is 1.22 or later, which covers all current Go releases.
>
> **Workaround / future work:** The intended fix is TLS termination in front of the proxy (e.g., a self-signed cert provisioned by `sandbox-manager`, or routing via an HTTPS gateway on the Lima host). Once the proxy is reachable over HTTPS, URL-embedded Basic Auth works as designed. Until then, sandbox provisioning scripts should not copy-paste the `GOPROXY` URL above and expect it to work without additional TLS setup.
>
> The E2E tests (`make test-e2e`) are currently skipped for the same reason.

## Security

- The bearer token is stored at `$XDG_CONFIG_HOME/local-gomod-proxy/auth-token` (default: `~/.config/local-gomod-proxy/auth-token`) with `0600` permissions.
- The token is auto-generated on first `serve` or `token` invocation. To rotate, delete the file and restart.
- The token's purpose is to block other machines on the Lima bridge from reaching the proxy. A compromised sandbox can only fetch modules it already has permission to download.
- Module paths are validated before any shell-out. No shell interpolation — `go mod download` is invoked via `exec.Command` with an argv slice.

## Development

```bash
make build              # Build binary to ./local-gomod-proxy
make test               # Run unit tests with race detector
make test-integration   # Run integration tests (-tags=integration)
make test-e2e           # Run E2E tests (-tags=e2e; currently skipped)
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

See [DESIGN.md](DESIGN.md) for the full architecture and design decisions.
