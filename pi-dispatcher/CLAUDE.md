# pi-dispatcher

Go CLI tool (`pd`) for dispatching, supervising, and inspecting autonomous Pi tasks in managed worktrees and the shared sandbox.

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

## Dashboard UI

The embedded dashboard in `internal/dashboard/index.html` should stay visually aligned with the MCP Broker dashboard. Preserve each dashboard's information architecture, but use the shared Agent Tools dashboard language:

- Dark blue-black base tokens: `--bg #080b10`, `--panel #101722`, `--panel-strong #151f2d`, `--panel-sunken #05070b`, `--text #e6edf3`, `--muted #8b98a8`, `--line #263244`.
- Product identity comes from `--product-accent` and `--product-accent-2`; Pi Dispatcher uses cyan/blue (`#7dd3fc`, `#38bdf8`).
- Use system UI and monospace font stacks; do not add external web-font dependencies.
- Keep shared component treatment consistent: 28px header icons, 12px panels/cards, 8px controls, subtle translucent panels, selected rows/tabs marked with the product accent, status dots with a soft glow, and pill chips/badges for compact state.
- Keep `favicon.svg` in the shared 64x64 rounded-square family, changing only the glyph/accent for the product identity.

Commands should delegate to internal packages. External commands must go through runner interfaces for tests.
