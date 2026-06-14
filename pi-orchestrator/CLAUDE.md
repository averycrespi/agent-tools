# pi-orchestrator

Go CLI tool (`po`) for coordinating typed, durable Pi workflows through `pd` task runs.

## Development

```bash
make build    # go build -o po ./cmd/po
make install  # go install ./cmd/po
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

```
cmd/po/             Cobra entry point and thin command wrappers
internal/workflow/  Workflow definition loading and validation
```

## Dashboard UI

The embedded dashboard in `internal/dashboard/index.html` should stay visually aligned with the MCP Broker and Pi Dispatcher dashboards. Preserve each dashboard's information architecture, but use the shared Agent Tools dashboard language:

- Dark blue-black base tokens: `--bg #080b10`, `--panel #101722`, `--panel-strong #151f2d`, `--panel-sunken #05070b`, `--text #e6edf3`, `--muted #8b98a8`, `--line #263244`.
- Product identity comes from `--product-accent` and `--product-accent-2`; Pi Orchestrator uses purple/cyan (`#c084fc`, `#7dd3fc`).
- Use system UI and monospace font stacks; do not add external web-font dependencies.
- Keep shared component treatment consistent: 28px header icons, 12px panels/cards, 8px controls, subtle translucent panels, selected rows/tabs marked with the product accent, status dots with a soft glow, and pill chips/badges for compact state.
- Keep `favicon.svg` in the shared 64x64 rounded-square family, changing only the glyph/accent for the product identity.

Commands should delegate to internal packages. External commands must go through runner interfaces for tests.
