# OAuth client credential persistence + tool-call retry shim

**Date:** 2026-04-21
**Scope:** `mcp-broker/internal/server/` (primarily `oauth.go`, `http.go`)

## Problem

OAuth credentials for the Datadog and Atlassian MCP backends expire in under a day. They are supposed to auto-refresh silently, but instead the user is kicked into a full browser OAuth flow daily.

## Root cause

The broker persists the OAuth refresh token to the OS keychain but does **not** persist the dynamic client registration (`client_id` / `client_secret` from RFC 7591). On every broker restart, `handler.RegisterClient()` runs again and issues a new `client_id`. The stored refresh token is bound to the _previous_ `client_id`, so the server rejects the refresh POST and the browser flow triggers.

Evidence:

- `internal/server/oauth.go:65-72` — `oauthConfig()` builds a fresh `transport.OAuthConfig` every startup with empty `ClientID` / `ClientSecret`.
- `internal/server/oauth.go:112-117` — `handler.RegisterClient()` runs whenever `GetClientID() == ""`, which is always true after restart.
- mcp-go v0.45.0 `client/transport/oauth.go:208-281` — `refreshToken` POSTs `grant_type=refresh_token` with `client_id = h.config.ClientID`; empty after restart → server rejects.

Secondary factor: Atlassian has a known refresh bug ([atlassian/atlassian-mcp-server#12](https://github.com/atlassian/atlassian-mcp-server/issues/12)) where refresh requests can fail transiently; retrying often succeeds.

## Design

Two layers, both small.

### Layer 1 — Client credential persistence

Persist the dynamic client registration alongside the token, keyed by server name.

**Storage layout:** reuse `keychainService = "mcp-broker"`; add a second keychain entry per server with account name `<serverName>.client`. Separate entry keeps the existing `Token` payload backwards-compatible (no migration needed).

| Keychain service | Account               | Value                                                        |
| ---------------- | --------------------- | ------------------------------------------------------------ |
| `mcp-broker`     | `<serverName>`        | `{access_token, refresh_token, expires_at, ...}` (unchanged) |
| `mcp-broker`     | `<serverName>.client` | `{client_id, client_secret}` (new)                           |

The `.client` suffix is distinct from any legal server name — server names don't contain dots in the current config schema.

**New type + helpers in `oauth.go`:**

```go
type clientCreds struct {
    ClientID     string `json:"client_id"`
    ClientSecret string `json:"client_secret,omitempty"`
}

func getClientCreds(serverName string) (*clientCreds, error) {
    data, err := keyring.Get(keychainService, serverName+".client")
    if err != nil {
        if errors.Is(err, keyring.ErrNotFound) {
            return nil, nil // first-run, not an error
        }
        return nil, fmt.Errorf("get client creds for %q: %w", serverName, err)
    }
    var creds clientCreds
    if err := json.Unmarshal([]byte(data), &creds); err != nil {
        return nil, fmt.Errorf("unmarshal client creds: %w", err)
    }
    return &creds, nil
}

func saveClientCreds(serverName string, creds clientCreds) error {
    data, err := json.Marshal(creds)
    if err != nil {
        return fmt.Errorf("marshal client creds: %w", err)
    }
    return keyring.Set(keychainService, serverName+".client", string(data))
}
```

**Design decisions:**

- **Free functions, not a `ClientStore` interface.** The existing `KeychainTokenStore` implements `transport.TokenStore` because mcp-go's config requires that interface; there's no analogous external contract for client creds (mcp-go exposes `GetClientID()` / `GetClientSecret()` accessors). Testing works the same via `keyring.MockInit()`. No second implementation is on the horizon; YAGNI.
- **`ErrNotFound` → `(nil, nil)`** distinguishes "first-run" from "keychain error." Callers check for `creds == nil`.

### Layer 1a — Seed on construction

`oauthConfig()` loads stored creds and seeds the mcp-go config before returning:

```go
func oauthConfig(serverName string) transport.OAuthConfig {
    port := callbackPort(serverName)
    cfg := transport.OAuthConfig{
        RedirectURI: fmt.Sprintf("http://localhost:%d/callback", port),
        TokenStore:  &KeychainTokenStore{serverName: serverName},
        PKCEEnabled: true,
    }
    if creds, err := getClientCreds(serverName); err != nil {
        fmt.Fprintf(os.Stderr, "load client creds for %q: %v\n", serverName, err)
    } else if creds != nil {
        cfg.ClientID = creds.ClientID
        cfg.ClientSecret = creds.ClientSecret
    }
    return cfg
}
```

On keychain error: log and continue with empty creds (graceful degradation, matches `KeychainTokenStore.GetToken` behavior). mcp-go will re-register — annoying but correct.

### Layer 1b — Persist after registration

`runOAuthFlow()` persists creds after `RegisterClient` succeeds:

```go
if handler.GetClientID() == "" {
    if err := handler.RegisterClient(ctx, "mcp-broker"); err != nil {
        return fmt.Errorf("register client: %w", err)
    }
    creds := clientCreds{
        ClientID:     handler.GetClientID(),
        ClientSecret: handler.GetClientSecret(),
    }
    if err := saveClientCreds(serverName, creds); err != nil {
        fmt.Fprintf(os.Stderr, "save client creds for %q: %v\n", serverName, err)
    }
}
```

**Signature change:** `runOAuthFlow` needs `serverName` as a parameter. Callers (`oauth.go:93`, `http.go:97`) already have `name` in scope.

**Observable behavior after this layer alone:**

- First auth ever: `RegisterClient` runs → creds saved → tokens saved. Existing browser flow unchanged.
- Subsequent restarts: `oauthConfig` loads saved creds → mcp-go's `GetClientID()` returns the stored value → `runOAuthFlow`'s `if handler.GetClientID() == ""` branch is skipped → refresh POST carries the correct `client_id` → silent success.
- Stored creds rejected by server (rotated / revoked registration): refresh fails → browser flow → new creds saved, overwriting stale ones. Self-healing.

### Layer 2 — Tool-call auto-retry shim

On `OAuthAuthorizationRequiredError` during a tool call, retry the call once. On the second attempt, mcp-go's `getValidToken` re-runs the refresh — which may succeed if the first failure was the transient Atlassian bug.

**Scope:** retry only. No mid-call browser fallback. If the retry also fails, propagate the error loudly so the caller sees a clear "re-auth required" failure. User recovers by restarting the broker (which will trigger the browser flow at startup via the existing `initializeOAuthClient` path).

**Implementation** — inline in `httpBackend.CallTool` and `httpBackend.ListTools` (shared by HTTP and SSE since `newSSEBackend` also returns `*httpBackend`):

```go
func (b *httpBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
    req := mcp.CallToolRequest{}
    req.Params.Name = name
    req.Params.Arguments = arguments

    resp, err := b.client.CallTool(ctx, req)
    if err != nil && isUnauthorized(err) {
        resp, err = b.client.CallTool(ctx, req) // single retry
    }
    if err != nil {
        return nil, err
    }
    return &ToolResult{Content: resp.Content, IsError: resp.IsError}, nil
}
```

Same shape for `ListTools`.

**Why inline vs. a `retryingBackend` wrapper:** two methods, three lines of retry each. A wrapper type would duplicate the entire `Backend` interface surface. Per project YAGNI rules, inline wins.

**Why no mid-call browser flow:** tool calls often originate from background agents; surprising the user with a browser popup mid-call is worse UX than a loud error they can act on.

## Rejected alternatives

- **Combined JSON blob (token + creds in one keychain entry).** Atomic but requires migration of existing stored tokens. Separate entry is simpler and keeps the existing `Token` payload format untouched.
- **`ClientRegistrationStore` interface.** No consumer benefit (one impl), no external contract to satisfy, testing already solved by `keyring.MockInit()`. Violates project YAGNI policy.
- **Warm-up refresh at startup.** Redundant — the existing `Initialize` call at startup already goes through mcp-go's OAuth transport, which triggers `getValidToken` and surfaces refresh failures as `OAuthAuthorizationRequiredError` → browser flow. An explicit `handler.GetAuthorizationHeader` call after Initialize would do nothing Initialize didn't already trigger.
- **Mid-call browser fallback on retry failure.** Disruptive when tool calls come from background agents. Loud error + manual restart is clearer.

## Non-goals

- No dashboard or CLI re-auth command — restart is the recovery path.
- No mutex for concurrent tool calls hitting the retry shim — best-effort retry may cause 1–2 duplicate refresh attempts under concurrency; not worth locking complexity.
- No structured backoff — single-shot retry.

## Testing

Unit tests in `internal/server/` (all use `keyring.MockInit()`):

1. `TestClientCreds_SaveAndGet` — round-trip, mirrors `TestKeychainTokenStore_SaveAndGet`.
2. `TestClientCreds_GetNoCreds` — unregistered server returns `(nil, nil)`, no error.
3. `TestOAuthConfig_SeedsFromStoredCreds` — save creds, call `oauthConfig("foo")`, assert `ClientID`/`ClientSecret` populated.
4. `TestOAuthConfig_EmptyWhenNoStoredCreds` — fresh mock keychain, assert returned config has empty `ClientID`.

**Open question:** unit-testing the retry shim requires a test seam (interface or func injection) to swap out `b.client`. For one test, the refactor may not be worth it — covered instead by manual verification below and by the existing happy-path tool-call coverage in E2E / integration tests.

**Manual verification after deploy:**

1. Delete stored keychain entries:
   ```bash
   security delete-generic-password -s mcp-broker -a atlassian
   security delete-generic-password -s mcp-broker -a atlassian.client  # expected: not found (first deploy)
   ```
2. Start broker → browser flow → auth → confirm both entries created:
   ```bash
   security find-generic-password -s mcp-broker -a atlassian.client
   ```
3. Restart broker → no browser flow, tool calls work.
4. Observe across a sleep/wake cycle + 24h → expected: no browser flow.

## Documentation updates

- `mcp-broker/CLAUDE.md` — add the `<serverName>.client` keychain convention alongside the existing token entry convention.
- No `README.md` / `DESIGN.md` changes — internal persistence fix, not user-visible.

## References

- Atlassian Rovo MCP auth: <https://support.atlassian.com/atlassian-rovo-mcp-server/docs/authentication-and-authorization/>
- Atlassian refresh bug: <https://github.com/atlassian/atlassian-mcp-server/issues/12>
- Datadog MCP: <https://docs.datadoghq.com/bits_ai/mcp_server/>
- RFC 7591 (dynamic client registration): <https://datatracker.ietf.org/doc/html/rfc7591>
