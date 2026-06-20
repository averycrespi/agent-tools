# Agent Mailbox

Agent Mailbox is a local durable mailbox for AI coding agents to send messages to the user. It stores messages in SQLite and exposes a CLI, stdio MCP server, and local dashboard.

## Build

```bash
make build
./agent-mailbox --help
```

## Storage

By default, messages are stored at:

```text
$XDG_STATE_HOME/agent-mailbox/mailbox.db
```

If `XDG_STATE_HOME` is unset, the fallback is:

```text
~/.local/state/agent-mailbox/mailbox.db
```

All commands that touch the store accept `--db-path <path>` to override the default.

## CLI

```bash
agent-mailbox send --sender agent --subject "Need input" --body "Please choose an option" --severity action_required --requires-response
agent-mailbox list --status new --limit 20
agent-mailbox read <message-id>
agent-mailbox ack <message-id> --actor avery
agent-mailbox resolve <message-id> --actor avery
```

Messages have three lifecycle statuses: `new`, `acknowledged`, and `resolved`. Ack and resolve operations append lifecycle events and are idempotent.

## MCP broker configuration

Run the stdio MCP server with:

```bash
agent-mailbox mcp
```

Example `mcp-broker` backend configuration:

```json
{
  "servers": {
    "mailbox": {
      "command": "agent-mailbox",
      "args": ["mcp"]
    }
  }
}
```

With that server name, brokered tools are exposed as:

- `mailbox.send_message`
- `mailbox.list_messages`
- `mailbox.get_message`
- `mailbox.ack_message`
- `mailbox.resolve_message`

## Dashboard

```bash
agent-mailbox dashboard
agent-mailbox dashboard --host 127.0.0.1 --port 8500 --no-open
```

The dashboard is loopback-only and token protected. It prints an authenticated URL like:

```text
Agent Mailbox Dashboard: http://localhost:8500/dashboard/?token=...
```

The token is stored at `$XDG_CONFIG_HOME/agent-mailbox/auth-token`, or `~/.config/agent-mailbox/auth-token` when `XDG_CONFIG_HOME` is unset. Delete that file to force a new token on the next dashboard start.

The dashboard supports structured filters for status, severity, channel, and `requires_response`; viewing details; acknowledging; resolving; and SSE snapshot updates. Free-text search is intentionally not included in v1.

## Security and privacy

Agent Mailbox is local-first: the dashboard validates loopback hosts and does not provide a remote multi-user access model. Messages are stored in a local SQLite database. Agents should not send raw credentials, private keys, or sensitive logs unless the user intentionally chooses to store that data locally.

## Current non-goals

The MVP does not include Streamable HTTP MCP, desktop/mobile notifications, Slack/Matrix bridges, attachment storage, archive/delete APIs, a background polling daemon, a frontend build system, or free-text dashboard search.
