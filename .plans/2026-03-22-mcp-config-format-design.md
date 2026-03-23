# MCP Broker Config: Server Entry Format Redesign

## Goal

Align the `servers` section of `mcp-broker`'s config file with the format used by Claude Code, Cursor, and other MCP clients. Only the server entries change — the rest of the config (`rules`, `port`, `audit`, `log`, `open_browser`) is untouched.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Server collection type | Map keyed by name | Matches Claude Code/Cursor format |
| Top-level key name | `servers` (unchanged) | Clear from context; broker config isn't shared with Claude Code |
| Transport type names | `streamable-http` (was `http`), `sse` kept as deprecated | Match Claude Code's naming |
| OAuth config | Remove entirely | MCP spec handles OAuth automatically via 401 + RFC 8414 discovery |
| Backwards compatibility | None needed | No other users of this tool |

## Before / After

### Before
```json
{
  "servers": [
    {
      "name": "echo",
      "command": "echo",
      "args": ["hello"],
      "env": { "DEBUG": "1" }
    },
    {
      "name": "github",
      "type": "http",
      "url": "https://api.github.com/mcp",
      "headers": { "Authorization": "Bearer $TOKEN" },
      "oauth": true
    },
    {
      "name": "remote",
      "type": "sse",
      "url": "https://example.com/events"
    }
  ]
}
```

### After
```json
{
  "servers": {
    "echo": {
      "command": "echo",
      "args": ["hello"],
      "env": { "DEBUG": "1" }
    },
    "github": {
      "type": "streamable-http",
      "url": "https://api.github.com/mcp",
      "headers": { "Authorization": "Bearer $TOKEN" }
    },
    "remote": {
      "type": "sse",
      "url": "https://example.com/events"
    }
  }
}
```

## Server Entry Fields

### Stdio (default, `type` omitted)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | yes | Executable to spawn |
| `args` | string[] | no | Command-line arguments |
| `env` | map[string]string | no | Environment variables; supports `$VAR` / `${VAR}` expansion |

### Streamable HTTP (`type: "streamable-http"`)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | Must be `"streamable-http"` |
| `url` | string | yes | MCP endpoint URL |
| `headers` | map[string]string | no | HTTP headers; supports `$VAR` expansion |

### SSE (`type: "sse"`, deprecated)

Same fields as streamable-http. Kept for servers that only support the legacy SSE transport.

## What Changes

### Config types (`internal/config/config.go`)

- `Config.Servers` changes from `[]ServerConfig` to `map[string]ServerConfig`
- `ServerConfig.Name` field removed (name is now the map key)
- `ServerConfig.OAuth` field removed
- `OAuthConfig` struct and its `UnmarshalJSON` removed
- `DefaultConfig()` returns empty map `map[string]ServerConfig{}` instead of `[]ServerConfig{}`
- `ServerConfig.Type` values: accept `"streamable-http"` and `"sse"` (reject old `"http"`)

### Server manager (`internal/server/manager.go`)

- Iteration changes from ranging over a slice to ranging over a map (key = name)
- Server name passed alongside config rather than read from `cfg.Name`

### OAuth handling (`internal/server/oauth.go` + `internal/server/http.go`)

The `mcp-go` library already handles 401 detection, RFC 8414 metadata discovery, dynamic client
registration, PKCE, and token exchange. Our code just configures and orchestrates the flow.

**Changes:**
- Remove `if srv.OAuth != nil` branching in `http.go` — always create OAuth-capable clients
  (`client.NewOAuthStreamableHttpClient` / `client.NewOAuthSSEClient`) for HTTP/SSE transports
- Remove `buildOAuthConfig` (config-driven overrides). Replace with a minimal `transport.OAuthConfig`
  containing only `TokenStore`, `RedirectURI`, and `PKCEEnabled`
- Remove `OAuthConfig` struct references throughout
- **Keep as-is**: `runOAuthFlow`, `KeychainTokenStore`, `callbackPort`, `initializeOAuthClient`,
  `startCallbackServer`, `openBrowser` — these are runtime mechanics that don't depend on config
- The separate `newOAuthHTTPBackend` / `newOAuthSSEBackend` functions can be merged into
  `newHTTPBackend` / `newSSEBackend` since all HTTP/SSE backends are now OAuth-capable

### Server manager signature (`internal/server/manager.go`)

- `NewManager` signature changes from `servers []config.ServerConfig` to `servers map[string]config.ServerConfig`
- Iteration changes from `for _, srv := range servers` to `for name, srv := range servers`
- `connect()` takes server name as a separate parameter (no longer on `ServerConfig`)
- All `srv.Name` references become the `name` variable from the map key

### Tests

- All test configs updated to use map format
- OAuth-specific config tests removed
- E2E tests updated

### Documentation

#### `mcp-broker/README.md`

- **Config example** (lines 58-98): Rewrite from array to map format, remove `name` fields,
  change `"type": "http"` to `"type": "streamable-http"`, remove `"oauth": true` entry
- **Servers table** (lines 100-113): Remove `name` row, update `type` description
  (`"streamable-http"` instead of `"http"`), remove `oauth` row
- **OAuth section** (lines 115-138): Remove entirely. Replace with a short note that OAuth is
  auto-detected when a server responds with 401 (tokens stored in OS keychain)

#### `mcp-broker/CLAUDE.md`

- Line 49: Remove `OAuth config supports "oauth": true (all defaults) or "oauth": {...}` convention
- Lines 50-51: Keep keychain and callback port conventions but reword to remove config framing
  (e.g. "OAuth tokens are stored in the OS keychain" stays, just drop "config" references)

#### `mcp-broker/DESIGN.md`

- No changes needed — describes architecture, not config format

#### `local-git-mcp/README.md` and `local-gh-mcp/README.md`

- No changes needed — their broker config examples already use the new map format
