# telegram-mcp

`telegram-mcp` is a minimal stdio MCP server for agent-to-human Telegram notifications. It intentionally supports only sending text messages to one configured chat.

## Setup

1. Create a bot with [@BotFather](https://t.me/BotFather) and save the token.
2. Start a chat with the bot.
3. Find your chat ID, for example by sending the bot a message and calling Telegram's `getUpdates` endpoint.
4. Export credentials on the host running `mcp-broker`:

```bash
export TELEGRAM_MCP_BOT_TOKEN="..."
export TELEGRAM_MCP_CHAT_ID="..."
```

## Install

```bash
make install
```

## Broker configuration

Add `telegram-mcp` as a stdio backend in `~/.config/mcp-broker/config.json`:

```json
{
  "servers": {
    "telegram": {
      "command": "telegram-mcp",
      "env": {
        "TELEGRAM_MCP_BOT_TOKEN": "$TELEGRAM_MCP_BOT_TOKEN",
        "TELEGRAM_MCP_CHAT_ID": "$TELEGRAM_MCP_CHAT_ID"
      }
    }
  },
  "rules": [
    { "tool": "telegram.send_message", "verdict": "allow" },
    { "tool": "*", "verdict": "require-approval" }
  ]
}
```

Agents will see the tool as `telegram.send_message` when the backend is named `telegram`.

## Tool

### `send_message`

Sends a text message to the configured chat.

Parameters:

```json
{
  "message": "Build finished and tests passed.",
  "parse_mode": "plain",
  "disable_notification": false
}
```

- `message` is required and must be at most 4096 characters.
- `parse_mode` is optional: `plain`, `HTML`, or `MarkdownV2`. Default is plain text.
- `disable_notification` is optional.

## CLI

```bash
telegram-mcp [--api-base URL] [--http-timeout 15s]
```

The command serves MCP over stdio. It does not expose an HTTP listener.

## Security notes

- The bot token and chat ID stay on the host side, usually in the broker process environment.
- The tool cannot send to arbitrary Telegram chats; `chat_id` is fixed by `TELEGRAM_MCP_CHAT_ID`.
- No media upload, file upload, receiving messages, or chat administration features are implemented.

## Development

```bash
make build
make test
make lint
make audit
```
