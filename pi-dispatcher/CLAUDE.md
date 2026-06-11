# pi-dispatcher

Go CLI tool (`pd`) for launching and managing background Pi coding-agent runs in worktrees and the shared sandbox.

## Development

```bash
make build    # go build -o pd ./cmd/pd
make install  # go install ./cmd/pd
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

```
cmd/pd/        Cobra entry point and thin command wrappers
internal/config XDG paths and JSON config
internal/store  SQLite task/run/event state
internal/exec   External command runner abstraction
internal/pi     Pi RPC JSONL framing
internal/control Unix socket protocol
```

Commands should delegate to internal packages. External commands must go through runner interfaces for tests.
