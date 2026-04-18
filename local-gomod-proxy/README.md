# local-gomod-proxy

A host-side Go module proxy that lets sandboxed agents resolve private Go dependencies without holding the host's git credentials. Public modules are forwarded to `proxy.golang.org`; private modules (matched by `GOPRIVATE` patterns) are fetched via the host's git credentials and served back to the sandbox.

> **Local-only. Do not expose to the public internet.** The proxy currently runs unauthenticated over plain HTTP. It is intended to listen on a host-local interface reachable only by a co-located sandbox (e.g. the Lima bridge). Binding it to a public interface would let anyone on the network resolve modules against your git credentials. Binding it to `0.0.0.0` on a laptop on a public Wi-Fi network is equivalent to exposing it. A future version will restrict the listen address to the sandbox bridge IP.

## Install

```bash
# From the local-gomod-proxy subdirectory:
make install

# Or from the repo root:
make install
```

### Go version requirements

- **Host** — the proxy shells out to the host's `go` binary (`go mod download`, `go env -json`), so the host must have a Go toolchain installed. Go ≥ 1.21 is recommended; the proxy logs a warning on older versions because modules that use the `toolchain` directive may fail to resolve.
- **Building the proxy** — `go.mod` pins a specific Go release. Any Go ≥ 1.21 can build it via the `toolchain` auto-fetch; you don't need that exact version on the host.
- **Sandbox client** — no specific requirement beyond a `go` binary that speaks the module proxy protocol (Go 1.11+).

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

## How the sandbox consumes it

The intended configuration inside the sandbox is:

```sh
export GOPROXY=http://host.lima.internal:7070/
export GOSUMDB=off
# Explicitly clear GOPRIVATE in the sandbox. If it is set (inherited from a shell
# profile, image template, or `go env -w`), matching modules bypass GOPROXY and
# try to clone directly from git — which fails because the sandbox has no
# credentials. Routing everything through the proxy is the whole point.
unset GOPRIVATE
go env -u GOPRIVATE  # clears any `go env -w` persisted value
```

`GOSUMDB=off` is acceptable in the sandbox because `go.sum` (committed to the repo) is the primary integrity check.

## Security

- **Run on a local-only interface.** This proxy is unauthenticated. Anyone who can reach its listen address can resolve modules against your host's git credentials. Do not bind it to a public interface, a VPN-reachable interface, or `0.0.0.0` on an untrusted network. The expected deployment binds to the host-side Lima bridge IP so only the sandbox VM can reach it.
- Module paths are validated before any shell-out. No shell interpolation — `go mod download` is invoked via `exec.Command` with an argv slice.

## Development

```bash
make build              # Build binary to ./local-gomod-proxy
make test               # Run unit tests with race detector
make test-integration   # Run integration tests (-tags=integration)
make test-e2e           # Run E2E tests (-tags=e2e)
make lint               # Run golangci-lint
make fmt                # Format with goimports
make tidy               # go mod tidy + verify
make audit              # tidy + fmt + lint + test + govulncheck
```

See [DESIGN.md](DESIGN.md) for the full architecture and design decisions.
