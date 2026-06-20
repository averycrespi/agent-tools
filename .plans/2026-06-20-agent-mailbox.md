# Agent Mailbox Plan

## Goal

Create a new local, durable mailbox service for AI coding agents and background agents to send messages to the user. The service should expose an MCP tool surface that can be placed behind `mcp-broker`, persist message state locally, provide a CLI for direct inspection and lifecycle updates, and include a local dashboard matching the style and conventions of the existing repo dashboards.

## Background / Repo Context

- This repo is a monorepo of independent Go tools. New tools should follow the checklist in `AGENTS.md`: create `<name>/go.mod`, copy an existing tool `Makefile` and `.golangci.yml`, scaffold `cmd/<binary>/main.go` plus `internal/` packages, write `README.md`, `DESIGN.md`, `CLAUDE.md`, add an `AGENTS.md` symlink, add the tool to the root `Makefile`, and run `go mod tidy`.
- Root build/test automation is driven by the `TOOLS` list in `Makefile` and by `go.work`.
- Existing stdio MCP server patterns are available in `local-git-mcp/cmd/local-git-mcp/root.go` and `pi-dispatcher/cmd/pd/mcp.go`: create a handler, create `mcpserver.NewMCPServer`, register tools, and call `mcpserver.ServeStdio`.
- `mcp-broker` supports backend servers over stdio by default, plus Streamable HTTP and SSE when configured via `type`. Server configuration fields are defined in `mcp-broker/internal/config/config.go`, and user docs describe the server map in `mcp-broker/README.md`.
- `mcp-broker` prefixes backend tools as `<server>.<tool>`, so a mailbox backend configured as `"mailbox"` will expose tools such as `mailbox.send_message`.
- SQLite persistence patterns already exist in `mcp-broker/internal/audit/audit.go` and `pi-dispatcher/internal/store/store.go`: create parent dirs, open SQLite, enable WAL, set a busy timeout where concurrent access is expected, initialize schema, and run additive migrations.
- Existing dashboard conventions to match:
  - `pi-dispatcher/cmd/pd/dashboard.go` and `pi-orchestrator/cmd/po/dashboard.go` expose a `dashboard` command with `--host`, `--port`, and `--no-open`, validate loopback hosts, print an authenticated dashboard URL, open the browser unless disabled, and shut down gracefully on SIGINT/SIGTERM.
  - `mcp-broker/cmd/mcp-broker/serve.go` mounts a dashboard under `/dashboard/`, redirects `/` to `/dashboard/`, protects it with token auth, and prints `Dashboard: http://localhost:<port>/dashboard/?token=<token>`.
  - Existing dashboards embed `internal/dashboard/index.html` and `favicon.svg`, serve API routes under `/dashboard/api/...` or under a stripped `/api/...` mux, serve SSE under `/dashboard/events`, and use a local auth token with bearer/cookie/query-token access.
  - The three dashboard UIs share a dark theme: `--bg #080b10`, `--panel #101722`, `--panel-strong #151f2d`, `--panel-sunken #05070b`, `--text #e6edf3`, `--muted #8b98a8`, `--line #263244`, product accent variables, radial accent background, 28px brand icon, sticky header, connection status dot, two-pane grid, rounded bordered panels, compact controls, and embedded vanilla HTML/CSS/JS rather than a frontend build pipeline.
- External patterns informing this design:
  - Maildir: durable local delivery, atomic write-then-rename semantics, and no reliance on a live daemon.
  - ntfy/Gotify/Apprise: useful outbound notification adapter models, but not canonical storage.
  - Desktop notifications: useful ephemeral attention hints, not durable delivery.
  - Slack/Matrix/actor mailboxes: useful concepts for channels, threads, sender provenance, statuses, and per-agent/task destinations.
  - Event sourcing: useful for auditability and replay; an append-only event table plus a materialized `messages` table is a good local compromise.

## Acceptance Criteria

- AC-1: A new Go module `agent-mailbox/` exists, is included in `go.work`, is included in the root `Makefile` `TOOLS` list, and has the standard per-tool scaffolding: `Makefile`, `.golangci.yml`, `README.md`, `DESIGN.md`, `CLAUDE.md`, and `AGENTS.md` symlink.
- AC-2: The `agent-mailbox` binary provides a stdio MCP server command suitable for `mcp-broker` backend configuration.
- AC-3: The MCP surface exposes exactly these MVP tools with explicit JSON-schema inputs and deterministic structured outputs: `send_message`, `list_messages`, `get_message`, `ack_message`, and `resolve_message`.
- AC-4: Messages are durably stored in SQLite at `$XDG_STATE_HOME/agent-mailbox/mailbox.db`, falling back to `~/.local/state/agent-mailbox/mailbox.db` when `XDG_STATE_HOME` is unset, with WAL enabled and `busy_timeout=5000`. All commands that touch the store accept `--db-path`, and `--db-path` overrides the default path.
- AC-5: `send_message` creates a durable message with server-generated ID, creation/update timestamps, sender, channel, optional thread ID, subject, body, severity, `requires_response`, status, and optional idempotency key. Reusing the same non-empty sender plus idempotency key returns the existing message without creating a duplicate.
- AC-6: Message lifecycle operations are persisted and queryable: new messages can be listed, individual messages can be read, messages can be acknowledged, and messages can be resolved. Lifecycle operations append `message_events` records with event type, timestamp, actor, and JSON payload.
- AC-7: A CLI surface exists for local user inspection and lifecycle operations without MCP: at minimum `agent-mailbox send`, `agent-mailbox list`, `agent-mailbox read <id>`, `agent-mailbox ack <id>`, and `agent-mailbox resolve <id>`.
- AC-8: A local authenticated dashboard exists at `agent-mailbox dashboard`, defaults to `127.0.0.1:8500`, and supports `--host`, `--port`, `--no-open`, and `--db-path`. It mounts UI at `/dashboard/`, redirects `/` to `/dashboard/`, exposes JSON APIs under `/dashboard/api/...`, exposes SSE snapshots under `/dashboard/events`, validates loopback hosts, prints an authenticated URL, and uses token/cookie/bearer auth consistent with the existing dashboards.
- AC-9: The dashboard visually matches the established repo dashboard convention: embedded `index.html` and `favicon.svg`, dark theme variables and radial accent background, header with brand icon/title/connection status, two-pane master/detail layout, compact structured filter controls, rounded bordered panels, status/severity badges, responsive single-column layout, vanilla JS, and no frontend build step.
- AC-10: Dashboard API/UI behavior supports listing and filtering messages with structured filters only, viewing message details, acknowledging a message, and resolving a message. The UI updates from SSE snapshot events or equivalent polling/SSE behavior without requiring a browser refresh. Free-text search is intentionally out of scope for v1.
- AC-11: Unit tests or integration tests cover store initialization, message creation, idempotent send behavior, list/get behavior, ack behavior, resolve behavior, lifecycle transition behavior, dashboard auth, dashboard API routes, SSE snapshots, and validation errors for malformed inputs.
- AC-12: User-facing docs explain installation/building, MCP broker configuration, CLI usage, dashboard usage/auth, storage location, lifecycle statuses, security/privacy posture, and current non-goals.
- AC-13: Repository-level build/test checks pass for the new module and continue to pass from the root for all tools touched by this work.

## Non-Goals / Out of Scope

- No Streamable HTTP MCP server, macOS/desktop notification integration, browser notification integration, ntfy/Gotify/Apprise adapter, Slack/Matrix bridge, or mobile push integration in the MVP.
- No multi-user or remote-network access model.
- No attachment storage in the MVP. The schema may leave room for future attachments, but the first implementation should not expose attachment APIs.
- No background polling daemon beyond the dashboard HTTP process itself; message delivery remains durable through direct CLI/MCP store writes.
- No destructive deletion required in the MVP; archive/delete can be deferred unless needed by tests or docs.
- No frontend build system, package manager, or generated SPA assets.
- No free-text dashboard search in v1; use structured filters only.

## Constraints

- Follow repo-wide Go tool conventions from `AGENTS.md` and copy the nearest existing tool patterns rather than introducing shared internal packages.
- Keep durable delivery independent from the dashboard process. If the dashboard is stopped, CLI and stdio MCP sends must still work.
- Prefer stdio MCP first because it matches existing broker backend patterns and is enough for agents to send and inspect messages.
- Use SQLite as canonical state. Notifications, when added later, must be best-effort hints rather than the source of truth.
- Keep the dashboard loopback-only and token protected, consistent with `pd`, `po`, and `mcp-broker`.
- Avoid storing secrets by default in examples and docs. Document that agents should not send raw credentials or sensitive logs unless the user intentionally chooses to do so.
- Use bounded reads with limit/offset or equivalent pagination so agents cannot accidentally dump unbounded message history into context.
- Preserve compatibility with root `make build`, `make test`, and `make lint` expectations. If root `make audit` is too slow or unavailable, run `make -C agent-mailbox audit` and document the gap; do not skip root build/test/lint for new integration work.

## Chosen Approach

Implement a new independent Go tool named `agent-mailbox` that provides a durable SQLite-backed mailbox and exposes it through CLI commands, a stdio MCP server, and a local authenticated dashboard. Every command opens the local SQLite store, performs its operation, and exits, except `agent-mailbox dashboard`, which serves a loopback-only HTTP dashboard over the same store.

Internally, use a hybrid persistence model: a query-optimized `messages` table stores current message state, while a `message_events` table records lifecycle changes such as creation, acknowledgement, and resolution. This gives straightforward query behavior now and enough audit/replay history for future notification adapters.

The dashboard should be implemented the same way as the existing repo dashboards: Go `embed` for `index.html` and `favicon.svg`, a small `internal/dashboard` HTTP handler, token auth, JSON endpoints, SSE snapshot events, and vanilla HTML/CSS/JS with the shared visual language.

## Design Decisions

- D1: Name the new tool `agent-mailbox` and the binary `agent-mailbox`. Rationale: describes the user-facing concept without tying it to a specific transport or broker.
- D2: Start with stdio MCP only for agent access. Rationale: `mcp-broker` already supports stdio backends and existing tools demonstrate the pattern.
- D3: Include `agent-mailbox dashboard` in the MVP. Rationale: the user wants the mailbox dashboard to match the three existing dashboards, and a mailbox is much more useful with direct visual triage.
- D4: Use SQLite under XDG state as the source of truth. Rationale: consistent with `pi-dispatcher` and `mcp-broker`, easy to query, durable, and simple to back up.
- D5: Use a hybrid `messages` + `message_events` schema. Rationale: simple current-state queries plus append-only auditability without overbuilding full event sourcing.
- D6: Treat notification adapters, including macOS desktop notifications, as future best-effort outputs. Rationale: desktop/push systems are useful for attention but cannot be the durable mailbox, can expose sensitive content, and are awkward to test deterministically.
- D7: Make `requires_response`, `severity`, `channel`, `thread_id`, and sender provenance first-class fields. Rationale: background agents need to distinguish FYI updates from user-action-needed messages and thread long-running work.
- D8: Use idempotency keys scoped by sender. Rationale: agents may retry MCP calls or scripts may be re-run; repeated sends with the same sender/key should return the existing message rather than creating duplicates.
- D9: Use two lifecycle statuses in the MVP: `new`, `acknowledged`, and `resolved`. Rationale: this is enough to distinguish unread/inbox, seen/accepted, and closed work without adding ambiguous `seen` or `archived` semantics.
- D10: Use port `8500` for the dashboard by default. Rationale: existing local dashboards use nearby defaults (`pd` 8300, `po` 8400), so `8500` continues the convention without colliding.

## State Paths and Auth

- Default database path: `$XDG_STATE_HOME/agent-mailbox/mailbox.db` or `~/.local/state/agent-mailbox/mailbox.db` when `XDG_STATE_HOME` is unset.
- Database override: every command that opens the store accepts `--db-path <path>`. The flag overrides the default. No config file is required for MVP.
- Dashboard token path: `$XDG_CONFIG_HOME/agent-mailbox/auth-token` or `~/.config/agent-mailbox/auth-token` when `XDG_CONFIG_HOME` is unset.
- Dashboard auth token: 32 random bytes hex-encoded, stored mode `0600` in a directory mode no broader than `0750`.
- Dashboard query-token auth must set the cookie and redirect to the same relative URL with the `token` query parameter removed while preserving any other query parameters, matching `mcp-broker` behavior.
- Dashboard cookie name: `agent-mailbox-auth`, scoped to `/dashboard/`, `HttpOnly`, `SameSite=Strict`, and intentionally not `Secure` because the dashboard is loopback HTTP.
- Add `agent-mailbox token rotate` only if it is cheap to mirror `pd`/`po`; otherwise document deleting the token file as a temporary MVP rotation mechanism. If implemented, include tests and docs.

## MCP Contract

All MCP tools should return a structured payload with a stable shape. If the MCP Go library only supports text content for results, encode compact JSON text and keep the schema documented and tested at the handler boundary.

### `send_message`

Input:

```json
{
  "sender": "required non-empty string",
  "subject": "required non-empty string",
  "body": "required non-empty string",
  "channel": "optional string, default inbox",
  "thread_id": "optional string",
  "severity": "optional enum: info|success|warning|error|action_required, default info",
  "requires_response": "optional boolean, default false",
  "idempotency_key": "optional string"
}
```

Output:

```json
{
  "message": { "id": "...", "status": "new", "...": "full message fields" },
  "created": true
}
```

If sender plus non-empty idempotency key already exists, return the existing message with `created: false`.

### `list_messages`

Input:

```json
{
  "status": "optional enum: new|acknowledged|resolved",
  "channel": "optional string",
  "sender": "optional string",
  "severity": "optional enum",
  "requires_response": "optional boolean",
  "limit": "optional integer, default 50, max 200",
  "offset": "optional integer, default 0"
}
```

Output:

```json
{
  "messages": [
    { "id": "...", "subject": "...", "status": "...", "...": "summary fields" }
  ],
  "limit": 50,
  "offset": 0,
  "next_offset": 50,
  "total": 123
}
```

### `get_message`

Input:

```json
{ "id": "required message id" }
```

Output:

```json
{
  "message": { "id": "...", "body": "...", "...": "full message fields" },
  "events": [
    {
      "type": "message.created",
      "created_at": "...",
      "actor": "...",
      "payload": {}
    }
  ]
}
```

### `ack_message`

Input:

```json
{ "id": "required message id", "actor": "optional string, default user" }
```

Output:

```json
{
  "message": {
    "id": "...",
    "status": "acknowledged",
    "...": "full message fields"
  },
  "changed": true
}
```

### `resolve_message`

Input:

```json
{
  "id": "required message id",
  "actor": "optional string, default user",
  "resolution": "optional string"
}
```

Output:

```json
{
  "message": {
    "id": "...",
    "status": "resolved",
    "...": "full message fields"
  },
  "changed": true
}
```

Validation/error semantics:

- Missing required fields, unknown statuses, unknown severities, negative offsets, or limits above 200 return clear validation errors.
- Missing message IDs return not-found errors.
- Idempotent no-op lifecycle calls return the current message with `changed: false` rather than failing when the target state is already reached.

## Lifecycle Semantics

Statuses:

- `new`: newly created and not yet acknowledged or resolved.
- `acknowledged`: user or agent has acknowledged the message but not resolved it.
- `resolved`: message no longer requires attention.

Allowed transitions:

| Operation | From `new`                                        | From `acknowledged`                       | From `resolved`        |
| --------- | ------------------------------------------------- | ----------------------------------------- | ---------------------- |
| `ack`     | set `acknowledged`, append `message.acknowledged` | no-op, `changed=false`                    | no-op, `changed=false` |
| `resolve` | set `resolved`, append `message.resolved`         | set `resolved`, append `message.resolved` | no-op, `changed=false` |

Event requirements:

- `message.created`: actor is sender; payload includes channel, severity, requires_response, thread_id when present, and idempotency_key when present.
- `message.acknowledged`: actor defaults to `user`; payload may be empty.
- `message.resolved`: actor defaults to `user`; payload includes `resolution` when provided.
- Lifecycle no-ops do not append duplicate lifecycle events.

## Dashboard Contract

Command:

```text
agent-mailbox dashboard --host 127.0.0.1 --port 8500 --no-open --db-path /tmp/mailbox.db
```

Required behavior:

- Validate `--host` with the stricter repo-safe behavior: accept `127.0.0.1`, `localhost`, and `::1` only when every resolved IP is loopback; reject empty hosts, all-interface binds such as `0.0.0.0` or `::`, non-loopback IPs, and any hostname resolution that includes a non-loopback IP.
- Ensure/load dashboard auth token and print `Agent Mailbox Dashboard: http://localhost:8500/dashboard/?token=<token>` for `127.0.0.1`.
- Open the printed URL unless `--no-open` is set.
- Gracefully shut down on SIGINT/SIGTERM with a finite timeout.
- Serve UI at `/dashboard/`, favicon at `/dashboard/favicon.svg`, SSE at `/dashboard/events`, and APIs under `/dashboard/api/...`.
- Redirect `/` to `/dashboard/`.
- Allow bearer token auth for API tests, query-token auth that sets the dashboard cookie, and cookie auth for subsequent browser requests.
- After successful query-token authentication, redirect to the same dashboard URL with only the `token` query parameter removed, matching `mcp-broker`/`pd` behavior so the token is not left in the browser address bar.

Required API endpoints:

- `GET /dashboard/api/messages?status=&channel=&sender=&severity=&requires_response=&limit=&offset=` returns the same envelope as `list_messages`. Do not add a `q` or free-text search parameter in v1.
- `GET /dashboard/api/messages/{id}` returns the same detail envelope as `get_message`.
- `POST /dashboard/api/messages/{id}/ack` acknowledges and returns the ack envelope.
- `POST /dashboard/api/messages/{id}/resolve` resolves and returns the resolve envelope.
- `GET /dashboard/events` sends SSE `snapshot` events containing a compact message summary snapshot. Polling snapshots every 1-2 seconds is acceptable for MVP.

Required UI behavior:

- Header: brand icon, `Agent Mailbox` title, connection status dot/label.
- Left/master pane: message list with filter controls for status, severity, channel, sender, and requires-response; visible badges for severity and status. Do not add free-text search in v1.
- Right/detail pane: selected message subject/body/metadata/events and Ack/Resolve actions where applicable.
- Empty states for no messages and no selected message.
- Error states for failed API calls.
- SSE connection updates the list/detail without full page reload; if SSE fails, show disconnected status and allow manual refresh or retry.
- Match existing dashboard styling conventions: shared dark variables, radial accent background, rounded panels, compact controls, monospace metadata where appropriate, responsive single-column layout under narrow widths.

## Implementation Notes

- Add `agent-mailbox/` as a new Go module: `module github.com/averycrespi/agent-tools/agent-mailbox`.
- Mirror scaffolding from nearby Go tools:
  - copy `Makefile` and `.golangci.yml` from a similar module and update binary/module names;
  - add `cmd/agent-mailbox/main.go` and Cobra command files;
  - add `CLAUDE.md` and symlink `AGENTS.md -> CLAUDE.md`.
- Suggested internal package shape:
  - `internal/config`: XDG state/config path resolution and `--db-path` handling.
  - `internal/auth`: dashboard token creation/loading, cookie/bearer/query-token middleware, optional token rotation.
  - `internal/store`: SQLite schema, migrations, and CRUD/lifecycle operations.
  - `internal/mailbox`: domain types, validation, status/severity constants, idempotency behavior.
  - `internal/tools`: MCP tool definitions and handlers.
  - `internal/dashboard`: embedded UI, favicon, JSON API handlers, SSE snapshots.
  - optional `internal/output`: CLI formatting helpers if needed.
- CLI command shape:
  - `agent-mailbox mcp` starts stdio MCP.
  - `agent-mailbox send --sender <sender> --subject <subject> --body <body> [--channel inbox --severity info --requires-response --thread-id ... --idempotency-key ...]` creates a message.
  - `agent-mailbox list [--status new --channel inbox --limit 50 --offset 0]` prints compact tabular output by default and JSON with `--json` if implemented.
  - `agent-mailbox read <id>` prints one message.
  - `agent-mailbox ack <id>` acknowledges one message.
  - `agent-mailbox resolve <id> [--resolution text]` resolves one message.
  - `agent-mailbox dashboard` serves the dashboard.
- Store behavior:
  - create parent dir with restrictive permissions consistent with nearby tools;
  - enable WAL;
  - set `busy_timeout=5000`;
  - initialize schema on first open;
  - implement additive migrations if schema evolution is needed during implementation;
  - use UTC timestamps;
  - return stable IDs as strings or integers, but choose one and use it consistently across CLI, MCP, dashboard, docs, and tests.
- Suggested schema concepts:
  - `messages(id, created_at, updated_at, sender, channel, thread_id, subject, body, severity, requires_response, status, idempotency_key)`;
  - unique index on `(sender, idempotency_key)` where `idempotency_key` is not null/empty;
  - `message_events(id, message_id, event_type, created_at, actor, payload_json)`.
- Root integration:
  - add `./agent-mailbox` to `go.work`;
  - add `agent-mailbox` to root `Makefile` `TOOLS`.
- Keep docs focused and non-duplicative:
  - `README.md`: what it does, install/build, MCP broker config including recommended broker allow rules for mailbox tools, CLI quick start, dashboard quick start, storage/security notes.
  - `DESIGN.md`: intended durable mailbox semantics, schema/state model, MCP/CLI/dashboard architecture, future notification adapters as intentional non-MVP work.
  - `CLAUDE.md`: development commands, package layout, gotchas, local test commands.
  - `docs/launchd.md` and `examples/launchd/agent-mailbox-dashboard.plist` only if the implementation adds launchd support in the first pass; otherwise mention that launchd setup is future work.

## Documentation Impact

- Add `agent-mailbox/README.md`, `agent-mailbox/DESIGN.md`, and `agent-mailbox/CLAUDE.md` because this is a new user-facing tool.
- Add `agent-mailbox/AGENTS.md` as a symlink to `CLAUDE.md`.
- Update root `Makefile` and `go.work` for build/test integration.
- Add dashboard usage/auth/storage details to `agent-mailbox/README.md`.
- Add MCP broker configuration docs to `agent-mailbox/README.md` showing both the `servers.mailbox` backend entry and recommended rules so background agents can call at least `mailbox.send_message` without blocking on approval. Mark read-only mailbox tools as safe to allow if desired; keep lifecycle mutating tools subject to the user's preferred policy.
- Consider adding `agent-mailbox/docs/launchd.md` and an example plist only if `agent-mailbox dashboard --no-open` is intended to be installed as a persistent user LaunchAgent in the MVP.
- Do not update `mcp-broker` docs unless the implementation adds broker-specific behavior beyond a normal stdio backend. The new tool README should include a broker config snippet instead.

## Testing / Verification

- V1 for AC-1/AC-13: run `go work sync` if needed, then `make build` from the repo root; expected result: all listed tools including `agent-mailbox` build successfully.
- V2 for AC-11/AC-13: run `make test` from the repo root; expected result: all module tests pass. Do not substitute only `make -C agent-mailbox test` unless there is a documented pre-existing unrelated root test failure with concrete evidence.
- V3 for AC-3/AC-6: run focused tests for MCP handlers or store-backed tool handlers proving the five MCP tools accept the documented inputs and return the documented envelopes, including validation and not-found errors.
- V4 for AC-4/AC-5: run store tests using a temp directory/database proving default/override DB path behavior, schema initialization, WAL/busy-timeout setup where inspectable, durable re-open behavior, timestamps, and idempotency by sender/key.
- V5 for AC-6/AC-11: run lifecycle tests covering ack/resolve from `new`, `acknowledged`, and `resolved`, including no-op behavior and event creation/non-creation.
- V6 for AC-7: manually run CLI commands against a temp `--db-path`: `send`, `list`, `read`, `ack`, and `resolve`; expected result: output and persisted status changes match the docs.
- V7 for AC-2/AC-3: manually start `agent-mailbox mcp` in a way compatible with the MCP library tests or add an integration test that lists registered tools; expected result: required tools are registered and callable.
- V8 for AC-8/AC-10: run dashboard handler tests proving auth redirects/cookies/bearer access, query-token redirects strip only the `token` parameter while preserving other query parameters, strict loopback host validation cases (`127.0.0.1`, `localhost`, `::1`, `0.0.0.0`, `::`, non-loopback IP, and mixed/non-loopback DNS if testable), API list/detail/ack/resolve behavior, structured filters-only behavior, unauthorized behavior, and SSE snapshot output.
- V9 for AC-9: test or inspect the embedded HTML for key UI/style markers also used by existing dashboards: dark theme variables, radial background, `/dashboard/api/messages`, `/dashboard/events`, `new EventSource`, brand/header/status dot, two-pane grid, responsive layout, and no external frontend bundle. Also perform a manual browser or rendered screenshot inspection comparing the mailbox dashboard against the existing dashboards for layout, spacing, contrast, badge readability, and responsive behavior.
- V10 for AC-8: manually run `agent-mailbox dashboard --db-path <tempdb> --no-open`, seed a message through CLI or MCP, visit the printed token URL, confirm the message appears, acknowledge and resolve it from the UI, and confirm persisted status changes through CLI/API.
- V11 for AC-12: review `README.md`, `DESIGN.md`, and `CLAUDE.md` to confirm they document usage, storage, broker config, dashboard auth, security, statuses, tests, and non-goals.
- V12 for formatting/linting: run `make fmt` and `make lint` from the repo root; expected result: no formatting or lint failures. If root lint exposes a pre-existing unrelated failure, document it with evidence and still run `make -C agent-mailbox lint` successfully.

## Risks and Mitigations

- Risk: Scope creep into macOS notifications, browser notifications, push notifications, or chat bridges. Mitigation: keep notification adapters explicitly out of MVP and document them as future extensions.
- Risk: Dashboard scope makes the first implementation too broad. Mitigation: keep the dashboard read/ack/resolve only, use polling SSE snapshots, and copy the existing embedded vanilla dashboard pattern rather than introducing a frontend framework.
- Risk: MCP output shape becomes free-text and hard for agents to consume. Mitigation: return compact JSON-like structured responses from handlers where supported and keep CLI formatting separate from MCP response structs.
- Risk: Agents dump too much mailbox history into context. Mitigation: require bounded `list_messages` defaults and maximum limits.
- Risk: Duplicate messages from retried background agents. Mitigation: implement sender-scoped idempotency keys in the store and test them.
- Risk: SQLite concurrency problems when multiple agents write concurrently. Mitigation: enable WAL, set busy timeout, keep transactions short, and test concurrent or repeated sends if practical.
- Risk: Sensitive content persists longer than expected. Mitigation: document storage location and privacy posture; avoid remote forwarding in MVP; do not add push adapters until redaction/retention policies are designed.
- Risk: Tool naming conflicts or confusing broker prefixing. Mitigation: document that the configured broker server name controls the prefix, e.g. `mailbox.send_message`.
- Risk: Dashboard styling drifts from repo conventions. Mitigation: copy the layout/theme structure from `pi-dispatcher` or `pi-orchestrator`, change only product accent colors and mailbox-specific panels, and add tests that assert key HTML/CSS/API markers.

## Assumptions

- The MVP should prioritize reliable local delivery from agents to the user, with dashboard triage included, but without macOS/desktop, browser, or external push notification adapters.
- `agent-mailbox` is an acceptable tool and binary name.
- SQLite via the same driver patterns used by existing Go tools is acceptable.
- Stdio MCP behind `mcp-broker` is the desired first integration point.
- A small CLI remains valuable even with the dashboard because it enables scripting, verification, and fallback inspection.
- Default dashboard port `8500` is acceptable and does not collide with existing local services.

## Handoff Summary

Implement `.plans/2026-06-20-agent-mailbox.md` by creating the new `agent-mailbox` Go tool, integrating it into the monorepo, and building a SQLite-backed mailbox with CLI, stdio MCP access, and a local authenticated dashboard that matches the repo’s existing dashboard design conventions. Complete only after the required MCP tools, CLI commands, dashboard routes/UI, durable store, idempotency behavior, lifecycle events, docs, and verification commands are all working with concrete evidence.

Suggested `/goal` objective:

```text
Implement .plans/2026-06-20-agent-mailbox.md. Complete only after every acceptance criterion is satisfied with concrete evidence from files, tests, build/lint commands, manual CLI/MCP checks, and dashboard API/UI verification.
```
