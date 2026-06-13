# sandbox-manager

Go CLI tool (`sb`) for managing a Lima VM sandbox.

## Development

```bash
make build    # go build -o sb ./cmd/sb
make install  # go install ./cmd/sb
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

```
cmd/
  sb/          CLI entry point (main.go)
  *.go         Cobra commands (thin wrappers, no logic)
pkg/
  sandbox/     Minimal public facade for sandbox consumers
internal/
  exec/        Runner interface abstracting os/exec
  config/      Config struct + XDG path functions
  lima/        Lima VM client (wraps limactl CLI)
  sandbox/     Orchestration service (lifecycle + provisioning + template rendering)
```

`sandbox` defines its own `LimaClient` interface. `cmd/` constructs dependencies in `root.go` and delegates to `sandbox.Service`.

## Conventions

- All external commands go through `exec.Runner` — tests mock this interface, no real limactl calls in unit tests
- Keep `cmd/` thin; command tests are appropriate for argument parsing, exit-code behavior, and user-facing command wiring that internal packages cannot cover cleanly
- `sb create` is idempotent: handles all VM states (not created, stopped, running)
- Provisioning script cleanup failures log warnings but don't halt (best-effort)
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- VM name is hardcoded as `sb` in the lima package
- Lima template is embedded via `//go:embed` in `internal/sandbox`
- The host UID is injected into the Lima template for mount permission compatibility; Lima does not support injecting the host GID
- gosec `nolint` directives on `os/exec`, file permissions, and `os.Open` are intentional for a CLI tool
- Example provisioning scripts live in `examples/provision/`; prefer self-contained (works on a bare sandbox), always idempotent (guard installs with `command_exists`/`dpkg -s`), single-purpose. Scripts that depend on another example script must check for the prerequisite upfront and fail fast with a clear error
