# Upgrade and compatibility

Audience: Operators upgrading Gateway and its clients

Purpose: Coordinate client/service upgrades without rewriting durable authority or replaying uncertain work.

## Upgrade sequence

1. Identify affected interfaces using the mappings below; retain explicit installation/service selections and [safety artifacts](installation-safety.md).
2. Upgrade service, CLI, API consumers and automation together. Reload browser tabs and sign in again after restart.
3. Discard old cursors and inspect current resources with the upgraded client. A rejected or undecodable response is not proof that a mutation failed. Retain uncertain input/key/precondition and resolve its outcome before new intent; never automatically replay.
4. Configure clients using the [client compatibility gate](access-control.md#consumer-compatibility-qualification-and-rollback). Source changes and CI do not qualify installed resources.

## Browser persistence cutover

After upgrading, reload open tabs to load the bundled Agent Gateway client. Old `mcp_gateway_session` browser sessions require fresh sign-in; they are not converted to `agent_gateway_session` authority. Exact-origin sign-in, session bootstrap and logout responses expire the supplied old host-only cookie. When both names exist, only the canonical cookie can select a session. Service restart still invalidates all in-memory sessions; this cutover does not rotate administrator credentials or migrate host state.

The shared theme control preserves valid `system`, `light` and `dark` preferences: `agent_gateway_theme` wins when valid; otherwise a valid `mcp_gateway_theme` preference is copied to it. The old key is removed only after a successful canonical write. If writes fail, the saved old preference remains available for a later load and theme changes remain usable in memory. If storage cannot be read, the page starts with the system theme; malformed values are ignored. Do not clear browser storage as a migration prerequisite.

Reload and sign-in never replay mutations. If an old tab reports version skew, rejection or an uncertain result, retain its input/key/precondition, reload, sign in and inspect current resources before any deliberate follow-up. Browser persistence naming does not change routes, layout, Origin/CSRF policy, session expiry or revocation. Host and client changes remain separate: follow [installation safety](installation-safety.md) and [client configuration compatibility](access-control.md#existing-sandbox-migration-and-conflicts), including the still-required legacy client exports.

## Browser location cutover

Agents uses `#/agents` and Audit Log uses `#/audit-log`; their old grouped paths are invalid. Valid former `#/principals` collection, create and detail links are replaced with canonical `#/agents` fragments, preserving supported filter/sort state and the outer URL query. Other retired browser paths have **no aliases or redirects**. This sidebar cleanup changes no API endpoints. Update bookmarks and browser automation to the canonical locations below. Other retired or invalid paths show a safe invalid-location notice and return to fixed navigation, not the corresponding resource.

| Old collection                | Canonical collection    |
| ----------------------------- | ----------------------- |
| `#/servers`                   | `#/mcp/servers`         |
| `#/catalog`                   | `#/mcp/tools`           |
| `#/access/principals`         | `#/agents`              |
| `#/grants`                    | `#/mcp/grants`          |
| `#/requests`                  | `#/mcp/access-requests` |
| `#/invocations`               | `#/mcp/invocations`     |
| `#/audit`, `#/activity/audit` | `#/audit-log`           |

Carry supported detail IDs and `/new` suffixes beneath the new collection. Server-owned destinations become `#/mcp/servers/{server-id}/operations/{id}`, `/auth-flows/{id}`, and `/descriptors/{id}`. Server `tab=activity` becomes `tab=operations`; status is the default and omits `tab=status`. Only declared destination-specific filters are accepted; copied valid filters remain supported, but cursors and secrets never belong in URLs. `#/agents` is canonical; `#/access/grants` and `#/access/requests` are retired by the MCP permission cutover below. `#/overview`, `#/sign-in`, `#/system`, System tabs and System create paths are unchanged. Hash routing remains in place; no pathname fallback is served.

The location cutover itself is not an installation or browser-persistence migration. Durable authority, ports, MCP ingress/self-service and OAuth callback identities remain compatible. Current browser persistence follows the cutover above; current root/service selections follow [installation safety](installation-safety.md), while manual client configuration retains its separate compatibility requirements.

## MCP invocation namespace cutover

Upgrade the service, standalone CLI, bundled browser, API clients and automation together, then reload open tabs. Sign in again if restart invalidated the session. This clean cutover retains API v2; no aliases, redirects, fallback requests or automatic mutation replay are provided.

| Retired interface                            | Canonical interface                              |
| -------------------------------------------- | ------------------------------------------------ |
| `/api/v2/invocations`                        | `/api/v2/mcp/invocations`                        |
| `/api/v2/invocations/{id}`                   | `/api/v2/mcp/invocations/{id}`                   |
| `agent-gateway invocation list`              | `agent-gateway mcp invocation list`              |
| `agent-gateway invocation get INVOCATION_ID` | `agent-gateway mcp invocation get INVOCATION_ID` |
| `#/activity/invocations`                     | `#/mcp/invocations`                              |
| `#/activity/invocations/{id}`                | `#/mcp/invocations/{id}`                         |

Carry supported filters and detail IDs to the new locations; do not copy cursors or live/pause state into URLs. **MCP → Invocations** replaces **Activity → Agents**. **Audit Log** is the shared history destination at `#/audit-log`, replacing `#/activity/audit`. `/api/v2/audit-events` and `audit list/get` are unchanged and still include administrator, system, and offline-maintenance attribution. This is not an actor restriction or protocol filter. Overview remains shared; its existing MCP tool summaries are not cross-protocol metrics.

Retired API paths return not found; retired CLI commands fail locally before authority acquisition. Old or invalid browser links show the existing safe notice and fixed signed-in Overview or signed-out Sign in fallback, never an inferred resource lookup. Explicitly navigate using the current menu or update the bookmark and reload. After any rejected or uncertain operation, inspect current state before deciding on a new action; changing the route or signing in is never permission to replay an invocation or mutation.

Invocation JSON, CLI machine output, filters, limits, cursor/generation rules, read-only behavior and retention are unchanged. Existing invocation and audit rows/IDs remain readable, including history retained in backups. No migration, table/column change, protocol discriminator or persisted rewrite is needed. Historical authorization and redacted captures remain evidence of their original attempt, not current policy or proof of safe retry. Missing terminal evidence still means unknown outcome. Separate audit production, `invocations` invalidation events, credential bytes, backup lineage, MCP ingress/self-service and callback identities are unchanged. See [invocation evidence](invocation-evidence.md) for safe interpretation.

## MCP permission namespace cutover

Upgrade the service, standalone CLI, bundled browser, API consumers, and automation together. Reload open browser tabs after the upgrade and sign in again if the service restarted. This is a coordinated clean cutover within API v2, not a storage or credential migration. The sidebar starts with Overview, Agents, Audit Log, and System; **MCP** contains Servers, Tools, Grants, **Requests**, and **Invocations**. MCP page titles include the protocol prefix; Requests opens **MCP Access Requests**. Access requests approve MCP permissions, not network traffic or queued calls.

| Retired interface                     | Canonical interface                       |
| ------------------------------------- | ----------------------------------------- |
| `/api/v2/grants` and `/{id}`          | `/api/v2/mcp/grants` and `/{id}`          |
| `/api/v2/grant-requests` and `/{id}`  | `/api/v2/mcp/grant-requests` and `/{id}`  |
| `/api/v2/grant-requests/{id}/approve` | `/api/v2/mcp/grant-requests/{id}/approve` |
| `/api/v2/grant-requests/{id}/reject`  | `/api/v2/mcp/grant-requests/{id}/reject`  |
| `/api/v2/grant-constraints/validate`  | `/api/v2/mcp/grant-constraints/validate`  |
| `agent-gateway grant ...`             | `agent-gateway mcp grant ...`             |
| `agent-gateway grant-request ...`     | `agent-gateway mcp grant-request ...`     |
| `#/access/grants`, `/{id}`, `/new`    | `#/mcp/grants`, `/{id}`, `/new`           |
| `#/access/requests` and `/{id}`       | `#/mcp/access-requests` and `/{id}`       |

Methods, flags, representations, filters, ordering, counts, ETags, and confirmation semantics are unchanged. Carry only supported query parameters into new bookmarks. Agents and credentials stay top-level; MCP server/catalog routes, ingress, callbacks, and `mcp_gateway` self-service tools are unchanged. IDs, descriptions, historical rows, policy, audit/event names, and backup lineage are preserved without a database migration.

Old API paths return not found, old CLI commands fail locally, and old browser links show the existing invalid-location notice with a fixed fallback. There are no aliases, redirects, compatibility retries, or automatic mutation replay. Recover an old link by explicitly navigating through the current menu or updating the bookmark, then inspect the intended resource. After a rejected or uncertain mutation, inspect current state before a deliberate new action; changing the URL is never permission to replay it. Approval still does not execute the original tool call.

## Operator v2 cutover

Upgrade standalone CLI binaries, API clients, JSON scripts, and the service together. This is an intentional operator breaking change, not an installation migration. The current executable uses only the new grammar; there are no v1 HTTP handlers, top-level server/catalog aliases, redirects, or compatibility completions.

| Previous operator interface                                                                               | Current interface                                                                                                                                                                                    |
| --------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/api/v1/servers` and every child                                                                         | `/api/v2/mcp/servers` and the corresponding child                                                                                                                                                    |
| `/api/v1/catalog`                                                                                         | `/api/v2/mcp/catalog`                                                                                                                                                                                |
| Other administrative `/api/v1/*` resources, including sessions, events, credentials, status, and backups  | Corresponding `/api/v2/*` resources, outside the MCP namespace                                                                                                                                       |
| `server ...`, `catalog ...`                                                                               | `mcp server ...`, `mcp catalog ...` in the current executable, including an explicitly renamed current binary                                                                                        |
| Server `auth-flows` API resources                                                                         | `oauth-flows` resources; the CLI subtree is `mcp server auth-flow ...`                                                                                                                               |
| Operator flow JSON `flow_state`                                                                           | `state`                                                                                                                                                                                              |
| Operator limits `s2_idempotency_records`                                                                  | `server_idempotency_records`                                                                                                                                                                         |
| Human-label server-status queries such as `Authorization required`                                        | Stable snake_case tokens such as `authorization_required`; presentation labels are unchanged                                                                                                         |
| Implicit legacy/table query modes and `representation=table`                                              | One ordinary collection shape and default policy regardless of filters                                                                                                                               |
| Descriptor `retired=include/exclude/only` and `representation=summary`                                    | Omit status for all, use `status=available/retired`, and explicit `projection=full/summary` (default full)                                                                                           |
| Bare operation, agent, grant, and request pages without counts                                            | Their ordinary normalized query pages always include exact `total_count` and `offset`; grants/requests use enriched collection items                                                                 |
| Insertion-order defaults for servers, catalog, descriptors, agents, grants, and operation/request history | Defaults listed in the [normalized collection contract](../design/public-contract.md#normalized-administrative-collections); default limit 50, MCP inventory/catalog/descriptor/operation maximum 50 |

`projection=active` remains an exclusive operation read with `{items,has_more}`. Other ordinary pages do not acquire invented totals. The CLI's `--retired` descriptor selector translates to the new status query; API clients must use the new grammar. Exact grant/request policy fields and member-resource shapes are unchanged.

After a service upgrade, reload an already-open browser tab to load the bundled client, and sign in again if its session expired or still uses the legacy cookie. Browser fragments follow the [location cutover](#browser-location-cutover) above; layout is unchanged. Discard old page cursors; reload starts a fresh traversal. A failed or uncertain mutation during version skew is **not** permission to retry: retain its input/key/precondition, inspect current resources with the upgraded client, and resolve the outcome before any deliberate same-intent action. Never retry merely because the old tab or CLI cannot decode a response.

Existing same-key server work retains its durable identity across the route rename, including conflicts and interrupted outcomes. Database/backup lineage, stored enums, bearer verifiers/prefixes, keyring identifiers/generations, roots/locks, ports, `/mcp`, OAuth callback identities, `mcp_gateway.*` tools/schemas, and explicit installed selections are not rewritten by the operator v2 cutover. Installation migration is complete and its command is retired; retain the [post-migration safety artifacts](installation-safety.md). Current [manual client configuration](access-control.md#configure-an-agent-client-manually) retains both `AGENT_GATEWAY_ENDPOINT` / `AGENT_GATEWAY_AGENT_TOKEN` and the retained `MCP_GATEWAY_ENDPOINT` / `MCP_GATEWAY_AGENT_TOKEN` aliases from one authority. No reinitialization, credential rotation, automatic relocation, live installation mutation, or external agent-config change is part of this cutover. Offline recovery now uses `maintenance verify-and-recover-storage` and `maintenance restore-backup BACKUP_ID`; see the [recovery command cutover](backup-and-recovery.md#recovery-command-cutover) for removed spellings and changed CLI JSON. Historical acceptance evidence remains historical, not current release qualification.

## Setup and maintenance CLI cutover

Use `init` (`initialize` remains a quiet alias), `doctor` instead of top-level `status`, and `agent` instead of `principal`. Initial CA setup belongs to init; `http ca create` is removed. Export writes `<data-dir>/http-ca.pem` unless `--output PATH` or `--stdout` is selected. `--json` chooses structured output. Exceptional stopped work uses only the four `maintenance` commands; inspect `--dry-run` and supply `--confirm` for unattended execution. Old command locations have no aliases. These grammar changes do not rename database tables, API endpoints, credential identities or installed resources.

<a id="principal-http-default-client-cutover"></a>

## Agent HTTP-default client cutover

Upgrade service, CLI and strict clients together and reload the browser. `/api/v2/http/defaults/{id}` GET/PATCH and `http-default-*` ETags are removed, with no alias or redirect. Read `/api/v2/principals/{id}` instead; PATCH `{"http_default":"allow"}` or any combination with `display_name`, `state`, and `visibility` using that response's agent ETag. Missing/stale preconditions fail closed; an old default ETag is never valid. Lists, creation and credential-operation responses include the same new `Principal` field. Creation optionally accepts `http_default` as `block` or `allow` and persists it atomically with the agent and default MCP grant; omission remains block. The browser starts at **Block requests**, offers **Allow requests**, and shows the saved value in Agent details. Default allow supplies no credential, tunnel permission or local/private access and does not override explicit blocks. HTTP-default CLI files use `http_default`, not `default`; JSON output is the complete `Principal`.

In **Edit agent**, **Save agent** commits all dirty fields in one request. State/default changes share one confirmation; cancel sends no write. Conflicts preserve the draft and require review of refreshed current values before accepting their revision. Unknown outcomes are never replayed: inspect current settings and deliberately discard the uncertain draft before forming new intent. Existing stored defaults and backups need no migration, reinitialization or credential rotation.
