# OAuth Support for mcp-broker

## Overview

Add OAuth 2.0 support for backend MCP servers, enabling mcp-broker to connect to OAuth-protected servers like GitHub, Atlassian, and Datadog. Leverages mcp-go's built-in OAuth support (PKCE, metadata discovery, token exchange, refresh) with a keychain-backed token store and inline browser auth flow.

## Config

Add an `OAuth` field to `ServerConfig`:

```go
type OAuthConfig struct {
    ClientID      string   `json:"client_id,omitempty"`
    ClientSecret  string   `json:"client_secret,omitempty"`
    Scopes        []string `json:"scopes,omitempty"`
    AuthServerURL string   `json:"auth_server_url,omitempty"`
}

type ServerConfig struct {
    // ... existing fields ...
    OAuth *OAuthConfig `json:"oauth,omitempty"`
}
```

**All fields are optional:**

- `client_id` — Override for providers that don't support dynamic client registration. If empty, dynamic registration is attempted automatically.
- `client_secret` — For confidential clients. Supports `$VAR` expansion (env-based secrets).
- `scopes` — OAuth scopes to request. If omitted, the server grants its default scopes.
- `auth_server_url` — Fallback when RFC 9728 metadata discovery doesn't work.

OAuth is enabled when the `oauth` field is present (non-nil pointer).

**Example configs:**

```json
{
  "name": "github",
  "type": "http",
  "url": "https://api.githubcopilot.com/mcp",
  "oauth": {}
}
```

```json
{
  "name": "atlassian",
  "type": "http",
  "url": "https://mcp.atlassian.com",
  "oauth": {
    "scopes": ["read:jira-work", "write:jira-work"]
  }
}
```

```json
{
  "name": "custom-server",
  "type": "http",
  "url": "https://mcp.example.com",
  "oauth": {
    "client_id": "my-app-id",
    "client_secret": "$CUSTOM_CLIENT_SECRET",
    "auth_server_url": "https://auth.example.com"
  }
}
```

## Token Storage (Keychain)

Tokens are stored in the OS keychain via `github.com/zalando/go-keyring` (cross-platform: macOS Keychain, Linux Secret Service, Windows Credential Manager).

Implements mcp-go's `TokenStore` interface:

```go
type KeychainTokenStore struct {
    serverName string
}

func (s *KeychainTokenStore) GetToken(ctx context.Context) (*Token, error) {
    data, err := keyring.Get("mcp-broker", s.serverName)
    if err == keyring.ErrNotFound {
        return nil, transport.ErrNoToken
    }
    if err != nil {
        return nil, fmt.Errorf("keychain get: %w", err)
    }
    var token transport.Token
    if err := json.Unmarshal([]byte(data), &token); err != nil {
        return nil, fmt.Errorf("unmarshal token: %w", err)
    }
    return &token, nil
}

func (s *KeychainTokenStore) SaveToken(ctx context.Context, token *Token) error {
    data, err := json.Marshal(token)
    if err != nil {
        return fmt.Errorf("marshal token: %w", err)
    }
    return keyring.Set("mcp-broker", s.serverName, string(data))
}
```

Service name: `"mcp-broker"`. Key: server name from config.

## OAuth Connection Flow

**Inline on first connect.** When `serve` encounters an OAuth backend with no token, it opens the browser and runs the callback server automatically. This blocks that backend's connection until auth completes — other non-OAuth backends connect normally.

### Flow

1. **Build mcp-go `OAuthConfig`** from our config struct:
   - `PKCEEnabled: true` (always, public client default)
   - `RedirectURI: "http://localhost:<deterministic-port>/callback"`
   - `TokenStore: &KeychainTokenStore{serverName: srv.Name}`
   - `ClientID`, `ClientSecret`, `Scopes`, `AuthServerMetadataURL` from config (if provided)
   - `ClientSecret` gets `$VAR` expansion via `os.ExpandEnv`

2. **Create OAuth client** using mcp-go constructors:
   - HTTP: `client.NewOAuthStreamableHttpClient(url, oauthConfig, opts...)`
   - SSE: `client.NewOAuthSSEClient(url, oauthConfig, opts...)`

3. **Handle `OAuthAuthorizationRequiredError`** (can occur at `Start()` or `Initialize()`):
   - Extract `OAuthHandler` from the error
   - If no `ClientID`, call `oauthHandler.RegisterClient(ctx, "mcp-broker")` for dynamic registration
   - Generate PKCE verifier + challenge via `client.GenerateCodeVerifier()` / `client.GenerateCodeChallenge()`
   - Generate state via `client.GenerateState()`
   - Get authorization URL via `oauthHandler.GetAuthorizationURL(ctx, state, codeChallenge)`
   - Start a local callback HTTP server on the redirect port
   - Open browser with `open` (macOS) / `xdg-open` (Linux)
   - Log to stderr: "Opening browser for authentication..." (stdout is MCP transport)
   - Block waiting for callback
   - Exchange code for token via `oauthHandler.ProcessAuthorizationResponse(ctx, code, state, codeVerifier)`
   - Retry the connection — token is now in keychain

4. **Subsequent connections** — `KeychainTokenStore.GetToken()` returns the cached token. mcp-go handles refresh automatically when the token expires.

### Callback Port

Deterministic port derived from server name hash, mapped to ephemeral range (10000-65535):

```go
func callbackPort(serverName string) int {
    h := fnv.New32a()
    h.Write([]byte(serverName))
    return int(h.Sum32()%(65535-10000)) + 10000
}
```

Consistent redirect URI ensures dynamic client registration works across restarts.

## File Layout

```
internal/
  server/
    oauth.go          # NEW: KeychainTokenStore, auth flow, callback server, port hash
    http.go           # MODIFIED: branch to OAuth constructors when oauth config present
  config/
    config.go         # MODIFIED: add OAuthConfig struct and field to ServerConfig
```

### Changes to existing files

**`http.go`** — Minimal branching at the top of each constructor:

```go
func newHTTPBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
    if srv.OAuth != nil {
        return newOAuthHTTPBackend(ctx, srv)
    }
    // ... existing code unchanged ...
}

func newSSEBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
    if srv.OAuth != nil {
        return newOAuthSSEBackend(ctx, srv)
    }
    // ... existing code unchanged ...
}
```

**`config.go`** — Add `OAuthConfig` struct and `OAuth *OAuthConfig` field to `ServerConfig`.

### New file: `oauth.go` (~200 lines)

- `KeychainTokenStore` — `GetToken`, `SaveToken` (~30 lines)
- `newOAuthHTTPBackend` — Build OAuthConfig, create client, handle auth error, initialize (~40 lines)
- `newOAuthSSEBackend` — Same for SSE transport (~40 lines)
- `runOAuthFlow` — PKCE + browser + callback server + token exchange (~80 lines)
- `callbackPort` — FNV hash to port number (~10 lines)

### New dependency

`github.com/zalando/go-keyring` — cross-platform keychain access.

## Error Handling

- If keychain is unavailable (no Secret Service on Linux, locked keychain on macOS), error surfaces at connection time with a clear message
- If browser can't be opened, print the authorization URL to stderr so the user can copy-paste
- If callback server port is in use, fail with "port <N> in use — another mcp-broker instance may be authenticating"
- Auth timeout: no explicit timeout — the user controls when they complete the browser flow. If they cancel (Ctrl+C), normal shutdown handles cleanup

## Testing

- `KeychainTokenStore` — unit test with mock keyring (go-keyring has a mock backend)
- `callbackPort` — deterministic, trivial to test
- OAuth flow — integration test with `//go:build integration` tag, requires a test OAuth server
- Config parsing — test that `"oauth": {}` produces non-nil pointer, missing field produces nil
