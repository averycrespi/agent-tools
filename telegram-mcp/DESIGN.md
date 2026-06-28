# telegram-mcp Design

## Motivation

Agents sometimes need a low-friction way to notify the human operator: a long-running task finished, a blocker needs attention, or a local workflow reached a decision point. The existing `mcp-broker` Telegram integration is for approval notifications inside the broker itself; it is not a general MCP tool exposed to agents.

`telegram-mcp` provides a separate, intentionally minimal MCP server for agent-to-human notifications. It runs behind `mcp-broker` as a stdio backend, keeps Telegram credentials on the host, and exposes only the ability to send a text message to one configured chat.

## Architecture

`telegram-mcp` is a stdio MCP server. A caller spawns it as a subprocess and communicates over stdin/stdout using MCP. The server makes outbound HTTPS requests to the Telegram Bot API and does not listen on any port.

```
Agent -> mcp-broker -> telegram-mcp --HTTPS--> Telegram Bot API -> configured chat
```

Configuration is environment-only:

| Variable                 | Required | Description                  |
| ------------------------ | -------- | ---------------------------- |
| `TELEGRAM_MCP_BOT_TOKEN` | yes      | Bot token from BotFather     |
| `TELEGRAM_MCP_CHAT_ID`   | yes      | Chat ID messages are sent to |

CLI flags:

| Flag             | Default                    | Description                             |
| ---------------- | -------------------------- | --------------------------------------- |
| `--api-base`     | `https://api.telegram.org` | Telegram Bot API base URL, mainly tests |
| `--http-timeout` | `15s`                      | Per-request Telegram API timeout        |

## Tools

### `send_message`

Sends a text message to the configured Telegram chat.

Parameters:

| Name                   | Required | Description                                         |
| ---------------------- | -------- | --------------------------------------------------- |
| `message`              | yes      | Message text, maximum 4096 characters               |
| `parse_mode`           | no       | `plain`, `HTML`, or `MarkdownV2`; defaults to plain |
| `disable_notification` | no       | Sends silently when true                            |

The tool returns JSON text with Telegram's `message_id`, `chat_id`, and `date` fields.

Annotation: additive/open-world, not read-only, not idempotent, not destructive.

## Security

The configured chat ID is fixed at server startup and is not accepted as a tool argument. This keeps the capability narrow: agents can message the configured operator, not arbitrary Telegram users or groups.

The server does not support file upload, media upload, contact lookup, chat membership changes, polling updates, or receiving messages. Those are deliberately out of scope for v1.

Credentials are read from environment variables supplied by the host-side broker configuration. They should not be copied into sandboxed agent environments.

## Design decisions

**Narrow server name, narrow tool surface.** The binary is named `telegram-mcp` because it owns the Telegram integration boundary, but its documented scope is only notifications.

**Fixed recipient.** A `chat_id` tool argument would make the server a broader Telegram client and create avoidable abuse potential. Fixed recipient is enough for agent-to-operator notifications.

**Reject long messages instead of splitting.** Telegram's text limit is 4096 characters. Silent splitting can make agent output look like multiple independent notifications and complicate auditing. Callers should summarize before sending.

**Plain text by default.** Markdown and HTML escaping is easy for agents to get wrong. Plain text avoids formatting errors; callers can opt into `HTML` or `MarkdownV2` when they know they need it.

**Separate from broker approvals.** The broker's Telegram integration has approval-specific message formatting and callback polling. This server has separate credentials and lifecycle so agent notifications do not couple to broker approval internals.

## Testing

Unit tests use `httptest.Server` as a fake Telegram API. They cover successful sends, request validation, Telegram error responses, MCP argument handling, context forwarding, and tool annotations.
