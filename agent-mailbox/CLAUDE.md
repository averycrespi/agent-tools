# agent-mailbox

Go CLI/MCP tool for a durable local mailbox that coding agents can use to send messages to the user.

## Development

```bash
make build    # go build -o agent-mailbox ./cmd/agent-mailbox
make install  # go install ./cmd/agent-mailbox
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing when practical.

## Architecture

```
cmd/agent-mailbox/      Cobra CLI commands, MCP stdio command, dashboard server
internal/config/        XDG state/config paths
internal/store/         SQLite message and lifecycle event persistence
internal/mailboxmcp/    MCP tool contract and handlers
internal/auth/          Dashboard token and auth middleware
internal/dashboard/     Embedded dashboard handler and static assets
```

## Dashboard UI

The embedded dashboard in `internal/dashboard/index.html` should stay visually aligned with the other Agent Tools dashboards:

- Dark blue-black base tokens: `--bg #080b10`, `--panel #101722`, `--panel-strong #151f2d`, `--panel-sunken #05070b`, `--text #e6edf3`, `--muted #8b98a8`, `--line #263244`.
- Agent Mailbox uses purple/pink accents (`#a78bfa`, `#f472b6`).
- Keep a 28px header icon, sticky header, connection status dot, two-pane master/detail layout, rounded bordered panels, compact controls, status/severity badges, responsive single-column layout, vanilla JS, and no frontend build step.
- Do not add free-text search for v1; use structured filters only.
