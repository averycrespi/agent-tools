# Upstream server configuration

Audience: Gateway operators configuring upstream MCP servers

Purpose: Configure servers, credentials, and OAuth without broadening trust.

This guide owns operator procedures for server configuration, durable catalog inspection, write-only static credentials, OAuth authorization, and runtime operations. Generated help owns exact syntax:

- `mcp-gateway server --help`
- `mcp-gateway catalog --help`

See [DESIGN](../../DESIGN.md) for the system design index and [Downstream servers](../design/downstream-servers.md) for normative transport, credential-authority, OAuth, catalog, and runtime semantics. See [Administrator CLI and local administration](administration.md) for shared authentication, input, output, confirmation, and retry rules. These are online workflows: start `mcp-gateway serve` first; a proven refused selected address reports the exact startup command.

## Keep the three server states distinct

Gateway reports desired configuration, durable catalog evidence, and active runtime state separately.

- **Desired configuration** is the durable operator intent, including whether the server is enabled and which transport/authentication form it should use.
- **Durable descriptors** record the last successfully normalized catalog and may remain current, stale, retired, or unavailable after runtime changes.
- **Active catalog and runtime state** are process-local publication facts. Only an active route can be called.

Do not infer callability from a desired `enabled` value or a durable descriptor. Always inspect the server, recent operations, and active catalog together when diagnosing availability.

## Register and inspect a server

Prepare one strict JSON document and create the server:

```bash
mcp-gateway server create --file PATH
mcp-gateway server list --limit 50
mcp-gateway server get SERVER_ID
```

Server definitions are secret-free. They select a closed stdio or Streamable HTTP transport, protocol policy, and safe configuration. Put static credentials or OAuth client secrets only through the separate write-only credential command.

Creation generates an idempotency key unless one is supplied. If the response is uncertain, retain the reported key and canonical input digest, read the server inventory, and deliberately replay only the exact same input when necessary. The CLI does not retry automatically.

## Update desired configuration

Use direct flags for display name or enabled state, or use one mutually exclusive strict file for a complete patch including transport:

```bash
mcp-gateway server update SERVER_ID --display-name NAME
mcp-gateway server update SERVER_ID --enable --yes
mcp-gateway server update SERVER_ID --file PATH --yes
```

Omitting `--etag` performs one validated server read and uses that exact strong value once. Supplying `--etag ETAG` pins the explicit value and skips the convenience read. A patch that changes `enabled` or `transport` requires consequence confirmation because it can withdraw a runtime or replace behavior. A display-name-only patch does not prompt. The CLI never refreshes a stale ETag or replays an update automatically. On conflict, read the current server, review the new state, and prepare fresh intent.

## Custom upstream HTTP headers

Streamable HTTP create files and complete update transports accept an optional `headers` string map. The web create/edit form provides **Custom HTTP headers** rows directly below **HTTP endpoint**, before **Protocol preference**, to add, edit, or remove entries. For example, this disabled GitHub server selects the requested toolsets:

```json
{
  "namespace": "github",
  "display_name": "GitHub",
  "enabled": false,
  "transport": {
    "kind": "streamable_http",
    "url": "https://api.githubcopilot.com/mcp/",
    "protocol_mode": "auto",
    "authentication": { "mode": "bearer" },
    "headers": {
      "X-MCP-Toolsets": "default,actions,gists,issues,labels,pull_requests,users"
    }
  }
}
```

Use `server create --file PATH`, then supply the bearer through the separate credential command. For updates, include the complete transport under `transport` in the patch file and use `server update SERVER_ID --file PATH --yes`. The API uses the same create/PATCH JSON.

**Non-secret values only.** These values are ordinary plaintext configuration, readable by administrators and included in SQLite and backups. Never put tokens, API keys, passwords, cookies, or other secrets here. There is no environment interpolation or per-call override. Authentication remains on the existing credential paths.

Omitted, `null`, or `{}` headers in a **complete replacement transport** clear custom headers. Omitting the PATCH `transport` preserves them. Removing every row in the web form clears them. Header changes use the current ETag/revision, fence and replace the old runtime, and refresh the catalog; old routes cannot continue using the previous toolset.

Limits are 16 entries, 128 ASCII bytes per name, 4096 bytes per value, and 8192 bytes total (sum of name and value lengths). Names must be HTTP tokens and unique ignoring case. Values may be empty but otherwise must be printable ASCII with no leading/trailing spaces, tabs, line breaks, or control characters. Accepted value bytes are sent unchanged. Final combined requests, including generated Host, Connection, Content-Length and User-Agent fields where applicable, remain bounded to 100 fields, 32768 name/value bytes, and 8192 bytes per value. Exceeding a bound fails before network handoff, never by truncating or overwriting fields.

The following names are reserved, case-insensitively:

- Prefixes `mcp-`, `proxy-`, `sec-`, `x-forwarded-`, `content-`, `accept-`, and `if-`.
- `authorization`, `authentication-info`, `www-authenticate`, `cookie`, `cookie2`, `set-cookie`, `set-cookie2`, `api-key`, `x-api-key`, `x-auth-token`, `x-access-token`, `x-authorization`.
- `host`, `connection`, `keep-alive`, `te`, `trailer`, `transfer-encoding`, `upgrade`, `expect`, `forwarded`, `via`, `x-real-ip`, `x-original-url`, `x-rewrite-url`, `x-http-method-override`.
- `origin`, `referer`, `accept`, `user-agent`, `range`, `cache-control`, `pragma`, `max-forwards`, `date`.

Configured custom headers are supported by both Overview and Servers reads; their presence alone does not flag a server for investigation.

These headers apply only to this server's MCP requests (all protocol modes, initialization, notifications, discovery, and calls). They are not sent to OAuth metadata, registration, or token endpoints, other servers, or browser response headers. Inbound client headers are never forwarded; configured headers do not relax URL, TLS, address, or no-redirect policy.

## OAuth compatibility settings

Server create files, complete transport replacements in server update files, and the web form's **Advanced OAuth settings** accept three optional fields under `transport.authentication`:

- `callback_uri`: the exact HTTP loopback redirect registered with the provider, including explicit port and canonical path. `localhost`, canonical IPv4 loopback, and `[::1]` are accepted; remote names, wildcard addresses, credentials, query, fragment, encoded paths, and ambiguous spellings are rejected. Omission uses the main numeric-loopback `/oauth/callback` listener.
- `auth_server_metadata_url`: an exact canonical HTTPS metadata location with a path and optional bounded query. This changes the fetch location for authorization and refresh, never issuer identity or protected-resource authority. Invalid metadata fails without fallback. Existing TLS, address, trusted-origin, no-redirect, size, and deadline controls still apply.
- `scopes`: initial scope tokens replacing resource-metadata defaults. Tokens are deduplicated and sorted, bounded to 64 entries, 256 bytes each, and 8192 bytes including separators. `[]` explicitly sends an empty scope value; omission uses metadata defaults. `request_offline_access` separately adds `offline_access` only when advertised. Token responses may narrow, never expand the requested set; later expansion requires a new foreground authorization, without tool-call replay.

Omit a field or supply `null` in a **complete replacement transport** to clear that override. Omitting `transport` from a PATCH leaves all its settings unchanged. In the web form, blank URLs clear overrides; unchecking explicit scopes restores metadata defaults. No credentials belong in these fields.

This disabled synthetic example illustrates all three settings together; its metadata URL and scopes are **not real Slack configuration**:

```json
{
  "namespace": "compatibility-fixture",
  "display_name": "Compatibility fixture",
  "enabled": false,
  "transport": {
    "kind": "streamable_http",
    "url": "https://resource.example/mcp",
    "protocol_mode": "auto",
    "authentication": {
      "mode": "oauth",
      "registration": { "mode": "dynamic", "issuer": null },
      "trusted_origins": [],
      "request_offline_access": false,
      "callback_uri": "http://localhost:3118/callback",
      "auth_server_metadata_url": "https://resource.example/custom/authorization-metadata",
      "scopes": ["fixture.read", "fixture.write"]
    }
  }
}
```

Save as a JSON file and use `mcp-gateway server create --file PATH`. For updates, put the complete `transport` object under `transport` in the patch file and use `mcp-gateway server update SERVER_ID --file PATH --yes`.

Registration, browser authorization, and code exchange use identical redirect bytes. Gateway acquires a separate numeric-loopback, callback-only listener before exposing the authorization URL. A port collision—including another Broker or flow—fails without random-port fallback: stop the conflicting listener and start a new flow. The default callback URI reuses the main callback route. Additional listeners never expose MCP, administration, health, or assets, and main `--allowed-host` configuration neither registers nor admits them. Listeners close on terminal state, cancellation, expiry, supersession, shutdown, and rejected late preparation. Configuration changes fence stale flows and incompatible token authority. Restart never resumes listeners or authorization pages.

Deterministic fixtures are not live Slack qualification. Use actual provider configuration and credentials only under separately authorized live testing.

## Start and monitor operations

Inspect operation history before starting more work:

```bash
mcp-gateway server operation list SERVER_ID
mcp-gateway server operation get SERVER_ID OPERATION_ID
mcp-gateway server operation start SERVER_ID --kind refresh_catalog
mcp-gateway server operation start SERVER_ID --etag ETAG --kind reload --yes
```

Operation start uses the direct `--kind` flag; it has no request-file form. The closed explicit operations are `reload`, `retry`, `refresh_catalog`, or `disconnect_credentials`. Reload and credential disconnect require confirmation. Omitted `--etag` performs one validated server read; explicit `--etag` skips that convenience read. Starts generate or accept an idempotency key, but the CLI does not poll automatically; use operation reads to observe progress. An uncertain start retains the key, ETag, and canonical input digest for read-before-replay recovery.

Gateway serializes lifecycle work per server and may reject rather than queue when bounded admission is full. A successful operation records the resulting safe state; it does not guarantee that a remote side effect can be undone.

## Replace static or OAuth client credentials

Credential replacement is a separate write-only, strict-file mutation; no secret argv flags exist:

```bash
mcp-gateway server credential replace SERVER_ID --file PATH --yes
mcp-gateway server credential replace SERVER_ID --etag ETAG --file PATH --yes
```

Omitted `--etag` performs one validated server read; an explicit exact value skips it.

The input must be one complete supported static-slot set or OAuth-client-secret form. Replacement always requires confirmation. Gateway never emits, logs, digests for display, or automatically replays the submitted secret. A native keyring operation may prompt, fail, or outlive cancellation.

If the result is uncertain, inspect the server and its operation history. Those safe reads may identify the current credential revision and operation state, but they cannot reveal which secret value became authoritative. Do not resubmit a secret merely because its one-time source is no longer available.

## Complete OAuth authorization

Inspect existing flow state, then start a foreground flow with an automatic or explicit current server ETag:

```bash
mcp-gateway server auth-flow list SERVER_ID
mcp-gateway server auth-flow get SERVER_ID FLOW_ID
mcp-gateway server auth-flow start SERVER_ID --open
mcp-gateway server auth-flow start SERVER_ID --etag ETAG --open
```

Flow start requires a prepared controlling terminal in human and JSON modes. It publishes the one-time authorization URL only to that sink; safe metadata goes to ordinary output. `--open` opens the validated URL explicitly without a referrer. Gateway does not retain or reconstruct a lost URL and never retries flow creation automatically.

The OAuth callback remains Gateway-owned, on its main origin by default or the configured callback-only loopback listener, never the frontend development origin. Flow state is process-local where sensitive and bounded where durable; restart interrupts nonterminal work. To cancel an eligible flow after reading its current state:

```bash
mcp-gateway server auth-flow cancel SERVER_ID FLOW_ID
```

Cancellation requires confirmation and remains subject to state races. It does not restore old authority or replay remote revocation.

Failed flows retain a bounded, secret-free diagnostic with the flow ID as its correlation ID, the failed stage, a stable reason, and—when Gateway received one—a safe HTTP status. Read it with `server auth-flow get` or the administrator UI. Diagnostics never contain authorization URLs, codes, tokens, client secrets, provider response bodies, headers, or native error text, and remain available only while the bounded flow record is retained.

## Inspect durable and active catalogs

In the browser, Servers, a server's Tools tab, and Catalog use Previous/Next to replace one page of at most 50 rows. Filters and column sorting search the entire collection, not just the displayed rows. Filters and sorting survive browser Back/Forward; changing them starts on page one. Shared links and reloads also start at the first matching page—page cursors are not bookmarks. A changed traversal restarts once with a notice; use Refresh after an error. Empty results keep their filters and Reset control. Counts describe the displayed page only, not a collection total.

Durable descriptor history belongs to one server:

```bash
mcp-gateway server descriptor list SERVER_ID --retired include
mcp-gateway server descriptor get SERVER_ID TOOL_ID
```

A durable descriptor is evidence, not a callability claim. It preserves normalized identity and revision history even when a server is disabled, unavailable, disconnected, or deleted.

API clients can request collection-wide descriptor matching with the opt-in table queries described in the [public contract](../design/public-contract.md#server-and-catalog-vocabulary). Table-query pages contain at most 50 full descriptors and no totals; follow `next_cursor` only on explicit navigation, keeping the same query. Do not combine these options with legacy `retired` or `representation=summary`. A `409 stale_cursor` requires discarding the traversal and starting again without a cursor. Here `available` means non-retired durable evidence, not current callability. Existing CLI list behavior is unchanged.

The active catalog reports currently published tools and page-level generation posture:

```bash
mcp-gateway catalog list
```

Only current active publication can supply a governed downstream capability. A stale durable catalog may help explain prior state but does not authorize or route a call. Catalog refresh failure can preserve safe stale evidence while withdrawing or retaining active publication according to the current runtime state; use server and operation reads together.

## Disconnect credentials or delete a server

Use the explicit `disconnect_credentials` operation when local credential authority must be invalidated without deleting the server. It withdraws active routes before local authority changes. Remote OAuth revocation is bounded best effort and failure never restores local authority.

Deletion is permanent and requires an automatically loaded or explicit exact server ETag plus confirmation:

```bash
mcp-gateway server delete SERVER_ID --yes
mcp-gateway server delete SERVER_ID --etag ETAG --yes
```

Deletion tombstones the identity, withdraws active routes, retires durable descriptors, and invalidates local credential domains. There is no force path, and scheduled cleanup cannot guarantee remote revocation. If cleanup remains pending, retry only the documented local cleanup operation; never interpret a tombstone as restored authority.

Return to the [documentation map](../README.md) or [Gateway README](../../README.md) for common workflows. Use [Access control](access-control.md) to decide which principals can discover and call active tools, and [Invocation evidence](invocation-evidence.md) to investigate attempted calls.
