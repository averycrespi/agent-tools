# telegram-mcp

Minimal stdio MCP server for sending Telegram notifications to a configured chat.

## Development

```bash
make build    # go build -o telegram-mcp ./cmd/telegram-mcp
make install  # go install ./cmd/telegram-mcp
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

Stdio MCP server. No inbound network listener, no local state, no config file.

```
cmd/telegram-mcp/      CLI entry point (Cobra)
internal/
  telegram/            Minimal Telegram Bot API client
  tools/               MCP tool definitions and handlers
```

## Conventions

- Keep the tool intentionally narrow: agent-to-human notification, not a general Telegram client.
- Credentials come from `TELEGRAM_MCP_BOT_TOKEN` and `TELEGRAM_MCP_CHAT_ID`.
- Do not add user-supplied `chat_id`; the configured chat is the security boundary.
- Default messages are plain text. Optional `HTML` and `MarkdownV2` parse modes are allowed only through the `parse_mode` parameter.
- Respect Telegram's 4096-character message limit; reject over-limit messages rather than splitting silently.
- Stdio MCP server setup follows the repo baseline: `mcpserver.NewMCPServer()` + `srv.AddTool(tool, handler.Handle)` + `mcpserver.ServeStdio(srv)`.
- `mcp-go`: use `req.GetArguments()` for tool call arguments.
- `send_message` is additive/open-world and non-idempotent: it sends an external notification.
