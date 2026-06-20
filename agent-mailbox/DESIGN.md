# Agent Mailbox Design

## Purpose

Agent Mailbox is a local, durable mailbox for AI coding agents and background agents to send messages to the user. It provides three access paths over one SQLite store:

- a CLI for direct local inspection and lifecycle updates,
- a stdio MCP server suitable for `mcp-broker`, and
- a loopback-only authenticated dashboard for triage.

Durable delivery must not depend on the dashboard process. CLI and MCP commands open the SQLite database, perform one operation, and exit.

## State

The default database path is `$XDG_STATE_HOME/agent-mailbox/mailbox.db`, or `~/.local/state/agent-mailbox/mailbox.db` when `XDG_STATE_HOME` is unset. Every command that touches state accepts `--db-path`, which overrides the default.

SQLite is opened with WAL mode and `busy_timeout=5000`. The schema has:

- `messages`: current query-optimized message state.
- `message_events`: append-only lifecycle records for creation, acknowledgement, and resolution.

Messages contain server-generated IDs, timestamps, sender, channel, optional thread ID, subject, body, severity, `requires_response`, status, and optional idempotency key. Non-empty idempotency keys are unique per sender so retried sends return the existing message.

## Lifecycle

The MVP statuses are:

- `new`: delivered and not yet acknowledged.
- `acknowledged`: seen/accepted by a user or local actor.
- `resolved`: closed.

Lifecycle operations are idempotent. Re-acknowledging an acknowledged message or re-resolving a resolved message returns the current message with `changed: false`.

## MCP

`agent-mailbox mcp` serves a stdio MCP server exposing exactly the MVP tools:

- `send_message`
- `list_messages`
- `get_message`
- `ack_message`
- `resolve_message`

Handlers return compact JSON text with deterministic object shapes because this matches the existing MCP patterns in the repo.

## Dashboard

`agent-mailbox dashboard` binds to `127.0.0.1:8500` by default, validates that the bind host resolves to loopback, prints an authenticated URL, and mounts:

- UI at `/dashboard/`,
- JSON APIs under `/dashboard/api/...`, and
- SSE snapshots under `/dashboard/events`.

The auth token is stored at `$XDG_CONFIG_HOME/agent-mailbox/auth-token` or `~/.config/agent-mailbox/auth-token`, mode `0600`. Auth accepts bearer token, dashboard cookie, or a `token` query parameter that sets the cookie and redirects to the same relative URL without the token.

## Non-goals

No Streamable HTTP MCP server, remote network access model, desktop/mobile notifications, attachment APIs, destructive delete/archive, frontend build pipeline, or free-text dashboard search are included in the MVP.
