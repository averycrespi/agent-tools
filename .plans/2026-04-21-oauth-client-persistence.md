# OAuth Client Credential Persistence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Stop the mcp-broker from kicking the user into a fresh browser OAuth flow daily by persisting the dynamic client registration (RFC 7591) alongside the refresh token, plus add a one-shot retry for transient 401s during tool calls.

**Architecture:** Two independent layers in `mcp-broker/internal/server/`. Layer 1 adds a second keychain entry per server (account `<serverName>.client`) holding `{client_id, client_secret}`; `oauthConfig()` seeds mcp-go's transport config from it at startup, and `runOAuthFlow()` writes to it after `handler.RegisterClient()` succeeds. Layer 2 retries a tool call once when the HTTP backend returns `OAuthAuthorizationRequiredError` — mcp-go's own refresh path runs on the retry, which works around the transient Atlassian refresh bug.

**Tech Stack:** Go, `github.com/mark3labs/mcp-go` (OAuth transport), `github.com/zalando/go-keyring` (OS keychain + `MockInit()` for tests), `github.com/stretchr/testify/require`.

**Design reference:** `.designs/2026-04-21-oauth-client-persistence.md` — source of truth. Code that disagrees with that document is the bug.

---

## Preconditions

- Working directory for all commands below is the repo root unless stated otherwise. `mcp-broker/` is a sub-module; its Makefile targets run from inside `mcp-broker/`.
- `make test` in `mcp-broker/` runs `go test -race ./...` and currently passes on `main`. Confirm before starting:

  ```bash
  cd mcp-broker && make test
  ```

  Expected: all tests pass.

---

## Task 1: Add `clientCreds` type and keychain helpers (Layer 1 storage)

Adds the storage primitives. Pure keychain round-trip — no mcp-go wiring yet.

**Files:**

- Modify: `mcp-broker/internal/server/oauth.go` (append after `KeychainTokenStore.SaveToken`, before `callbackPort`)
- Modify: `mcp-broker/internal/server/oauth_test.go` (append after `TestKeychainTokenStore_GetToken_CorruptedToken`)

**Step 1: Write the failing tests**

Append to `mcp-broker/internal/server/oauth_test.go`:

```go
func TestClientCreds_SaveAndGet(t *testing.T) {
	err := saveClientCreds("test-server", clientCreds{
		ClientID:     "cid-123",
		ClientSecret: "csecret-456",
	})
	require.NoError(t, err)

	got, err := getClientCreds("test-server")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cid-123", got.ClientID)
	require.Equal(t, "csecret-456", got.ClientSecret)
}

func TestClientCreds_GetNoCreds(t *testing.T) {
	got, err := getClientCreds("unregistered-server")
	require.NoError(t, err)
	require.Nil(t, got)
}
```

**Step 2: Run tests to verify they fail**

```bash
cd mcp-broker && go test -race ./internal/server -run 'TestClientCreds' -v
```

Expected: compile error — `undefined: saveClientCreds`, `undefined: getClientCreds`, `undefined: clientCreds`.

**Step 3: Implement the helpers**

In `mcp-broker/internal/server/oauth.go`, add the following block after the `KeychainTokenStore` methods (right before the `callbackPort` function):

```go
// clientCreds holds the dynamic client registration (RFC 7591) issued by
// the OAuth server. Persisting it across restarts lets mcp-go reuse the
// same client_id when refreshing tokens; otherwise the server rejects the
// refresh because the stored refresh_token was bound to a prior registration.
type clientCreds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// getClientCreds returns the stored dynamic client registration for a server,
// or (nil, nil) if none has been saved yet (first-run).
func getClientCreds(serverName string) (*clientCreds, error) {
	data, err := keyring.Get(keychainService, serverName+".client")
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get client creds for %q: %w", serverName, err)
	}
	var creds clientCreds
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return nil, fmt.Errorf("unmarshal client creds: %w", err)
	}
	return &creds, nil
}

// saveClientCreds persists the dynamic client registration for a server.
func saveClientCreds(serverName string, creds clientCreds) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal client creds: %w", err)
	}
	return keyring.Set(keychainService, serverName+".client", string(data))
}
```

Imports already satisfied by the existing `encoding/json`, `errors`, `fmt`, and `go-keyring` imports at the top of the file. No new imports needed.

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test -race ./internal/server -run 'TestClientCreds' -v
```

Expected: `TestClientCreds_SaveAndGet` and `TestClientCreds_GetNoCreds` both PASS.

Also run the full server-package test to confirm no regressions:

```bash
cd mcp-broker && go test -race ./internal/server
```

Expected: PASS.

**Step 5: Commit**

```bash
git add mcp-broker/internal/server/oauth.go mcp-broker/internal/server/oauth_test.go
git commit -m "feat(mcp-broker): persist OAuth dynamic client registration"
```

---

## Task 2: Seed `oauthConfig()` from stored creds (Layer 1a)

Wire the new storage into the mcp-go transport config so that restart reuses the stored `client_id`.

**Files:**

- Modify: `mcp-broker/internal/server/oauth.go:65-72` (the `oauthConfig` function)
- Modify: `mcp-broker/internal/server/oauth_test.go` (append two tests)

**Step 1: Write the failing tests**

Append to `mcp-broker/internal/server/oauth_test.go`:

```go
func TestOAuthConfig_SeedsFromStoredCreds(t *testing.T) {
	err := saveClientCreds("seeded-server", clientCreds{
		ClientID:     "stored-cid",
		ClientSecret: "stored-secret",
	})
	require.NoError(t, err)

	cfg := oauthConfig("seeded-server")
	require.Equal(t, "stored-cid", cfg.ClientID)
	require.Equal(t, "stored-secret", cfg.ClientSecret)
}

func TestOAuthConfig_EmptyWhenNoStoredCreds(t *testing.T) {
	cfg := oauthConfig("no-creds-server")
	require.Empty(t, cfg.ClientID)
	require.Empty(t, cfg.ClientSecret)
}
```

Note: each test uses a unique server name so Task 1's tests and these don't cross-contaminate via the shared mock keychain state.

**Step 2: Run tests to verify they fail**

```bash
cd mcp-broker && go test -race ./internal/server -run 'TestOAuthConfig_Seeds|TestOAuthConfig_EmptyWhenNoStoredCreds' -v
```

Expected: `TestOAuthConfig_SeedsFromStoredCreds` FAILS — `cfg.ClientID` is empty because `oauthConfig` doesn't read stored creds yet. `TestOAuthConfig_EmptyWhenNoStoredCreds` may already pass (empty is the current behavior), which is fine.

**Step 3: Update `oauthConfig`**

Replace the entire `oauthConfig` function in `mcp-broker/internal/server/oauth.go` with:

```go
// oauthConfig creates an OAuth config for mcp-go's transport.
// It seeds ClientID/ClientSecret from stored creds if available, so that
// refresh POSTs after restart carry the correct client_id. On keychain
// error, it logs and returns an empty-creds config (graceful degradation:
// mcp-go will re-register, which triggers a browser flow but is correct).
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

**Step 4: Run tests to verify they pass**

```bash
cd mcp-broker && go test -race ./internal/server -v
```

Expected: all tests PASS, including the new `TestOAuthConfig_SeedsFromStoredCreds` and the existing `TestOAuthConfig_RedirectURIMatchesCallbackPort` (unchanged fields).

**Step 5: Commit**

```bash
git add mcp-broker/internal/server/oauth.go mcp-broker/internal/server/oauth_test.go
git commit -m "feat(mcp-broker): seed OAuth config from persisted client creds"
```

---

## Task 3: Persist creds after dynamic client registration (Layer 1b)

Write the newly-issued creds to the keychain so they survive restart. Requires a signature change to `runOAuthFlow` because it needs `serverName` to key the storage.

**Files:**

- Modify: `mcp-broker/internal/server/oauth.go:93` (caller) and `oauth.go:106-117` (the `runOAuthFlow` function)
- Modify: `mcp-broker/internal/server/http.go:97` (caller)

**Note:** there is no straightforward unit test here — it would require a fake OAuth handler. The design's explicit decision is to cover this path via the manual verification checklist in the design doc (and via the existing compiler + regression tests). Do not invent a test seam for it.

**Step 1: Update the `runOAuthFlow` signature**

In `mcp-broker/internal/server/oauth.go`, change:

```go
func runOAuthFlow(ctx context.Context, authErr error, port int) error {
```

to:

```go
func runOAuthFlow(ctx context.Context, authErr error, serverName string) error {
	port := callbackPort(serverName)
```

Delete the existing `port int` parameter and the now-redundant outer `port` variable if any — the body should continue to reference `port` exactly as before.

**Step 2: Persist creds after `RegisterClient` succeeds**

Still in `runOAuthFlow`, replace the current block:

```go
// Dynamic client registration if no client ID
if handler.GetClientID() == "" {
    if err := handler.RegisterClient(ctx, "mcp-broker"); err != nil {
        return fmt.Errorf("register client: %w", err)
    }
}
```

with:

```go
// Dynamic client registration if no client ID. Persist the resulting
// client_id/client_secret so refresh flows after restart carry the right
// client_id — otherwise the server rejects refresh and we fall back to
// a full browser flow daily.
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

Save errors are logged-and-continued to match the surrounding pattern (`KeychainTokenStore.GetToken`, the seeding path in Task 2). The browser flow has already succeeded at this point; losing persistence just means the next restart will re-register, which is annoying but correct.

**Step 3: Update callers**

In `mcp-broker/internal/server/oauth.go` at the call site currently on line 93:

```go
if err := runOAuthFlow(ctx, err, callbackPort(name)); err != nil {
```

change to:

```go
if err := runOAuthFlow(ctx, err, name); err != nil {
```

In `mcp-broker/internal/server/http.go` at the call site currently on line 97:

```go
if err := runOAuthFlow(ctx, err, callbackPort(name)); err != nil {
```

change to:

```go
if err := runOAuthFlow(ctx, err, name); err != nil {
```

**Step 4: Verify the package still builds and all tests pass**

```bash
cd mcp-broker && go build ./... && go test -race ./internal/server -v
```

Expected: build succeeds, all tests PASS (no test covers this path, but we're verifying we haven't broken signatures anywhere).

Also run a grep to make sure no other caller of `runOAuthFlow` was missed:

```bash
cd mcp-broker && grep -rn 'runOAuthFlow(' internal/ cmd/ test/ 2>/dev/null
```

Expected: exactly the two call sites edited above, both passing `name` (a `string`).

**Step 5: Commit**

```bash
git add mcp-broker/internal/server/oauth.go mcp-broker/internal/server/http.go
git commit -m "feat(mcp-broker): save client creds after dynamic registration"
```

---

## Task 4: Add tool-call retry shim for transient 401s (Layer 2)

Retry `CallTool` and `ListTools` once when the HTTP backend returns a 401 / `OAuthAuthorizationRequiredError`. On the second attempt, mcp-go's `getValidToken` re-runs the refresh, which often succeeds after a transient Atlassian failure. No mid-call browser fallback — propagate the error on second failure so the caller restarts the broker.

**Files:**

- Modify: `mcp-broker/internal/server/http.go:129-169` (`ListTools` and `CallTool`)

**Note:** per the design's "Open question" paragraph, no unit test — testing the retry would require injecting a fake `*client.Client`, which is a bigger refactor than three lines of retry warrants. Rely on the existing happy-path tests plus the manual verification checklist.

**Step 1: Add the retry to `CallTool`**

In `mcp-broker/internal/server/http.go`, replace the body of `CallTool`:

```go
func (b *httpBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	resp, err := b.client.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}
```

with:

```go
func (b *httpBackend) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	resp, err := b.client.CallTool(ctx, req)
	if err != nil && isUnauthorized(err) {
		// Retry once: mcp-go's getValidToken re-runs refresh, which often
		// succeeds after a transient Atlassian refresh failure. If the
		// retry also fails, surface the error — user restarts the broker.
		resp, err = b.client.CallTool(ctx, req)
	}
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		Content: resp.Content,
		IsError: resp.IsError,
	}, nil
}
```

**Step 2: Add the retry to `ListTools`**

Still in `mcp-broker/internal/server/http.go`, replace the first few lines of `ListTools`:

```go
func (b *httpBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}
```

with:

```go
func (b *httpBackend) ListTools(ctx context.Context) ([]Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := b.client.ListTools(ctx, req)
	if err != nil && isUnauthorized(err) {
		resp, err = b.client.ListTools(ctx, req) // single retry — see CallTool
	}
	if err != nil {
		return nil, err
	}
```

Leave the rest of `ListTools` (tool-flattening loop) untouched.

**Step 3: Verify the package still builds and tests pass**

```bash
cd mcp-broker && go build ./... && go test -race ./internal/server -v
```

Expected: build succeeds, all existing tests PASS.

**Step 4: Commit**

```bash
git add mcp-broker/internal/server/http.go
git commit -m "feat(mcp-broker): retry tool calls once on transient 401"
```

---

## Task 5: Document the new keychain convention

Update `mcp-broker/CLAUDE.md` so future Claude sessions know about the second keychain entry per server.

**Files:**

- Modify: `mcp-broker/CLAUDE.md` (the OAuth line under `## Conventions`)

**Step 1: Locate the existing convention line**

The existing line reads:

```
- OAuth is auto-detected via 401 responses; tokens stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
```

**Step 2: Replace it**

Replace that single bullet with:

```
- OAuth is auto-detected via 401 responses; tokens stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- OAuth dynamic client registration (RFC 7591) is persisted in a second keychain entry per server (service: `mcp-broker`, key: `<serverName>.client`) so that refresh tokens survive restart — without it, every restart re-registers and the server rejects the prior refresh token
- Tool-call retry: `httpBackend.CallTool` and `ListTools` retry once on `isUnauthorized(err)` to work around transient refresh failures (e.g. [atlassian/atlassian-mcp-server#12](https://github.com/atlassian/atlassian-mcp-server/issues/12)); second failure propagates
```

Do not touch any other bullet.

**Step 3: Run the full audit to confirm nothing regressed**

```bash
cd mcp-broker && make audit
```

Expected: tidy clean, fmt clean, lint clean, all tests PASS, govulncheck clean.

**Step 4: Commit**

```bash
git add mcp-broker/CLAUDE.md
git commit -m "docs(mcp-broker): note client-creds keychain entry and retry shim"
```

---

## Manual verification (post-merge, not part of the commit sequence)

These steps are copied from the design doc and run on a host that actually has Atlassian / Datadog MCP backends configured. Not part of the plan's commit sequence but mandatory before closing the ticket:

1. Delete stored keychain entries for a real backend (e.g. `atlassian`):
   ```bash
   security delete-generic-password -s mcp-broker -a atlassian
   security delete-generic-password -s mcp-broker -a atlassian.client  # "not found" on first run is expected
   ```
2. Start the broker. Expect a browser flow. After auth, confirm both keychain entries exist:
   ```bash
   security find-generic-password -s mcp-broker -a atlassian
   security find-generic-password -s mcp-broker -a atlassian.client
   ```
3. Restart the broker. Expect **no** browser flow; confirm a sample Atlassian tool call still succeeds.
4. Leave running across a sleep/wake cycle + 24h. Expect no browser flow.

---

## Notes for the executor

- Do NOT introduce a `ClientRegistrationStore` interface — the design explicitly rejects it (YAGNI; one consumer, no external contract).
- Do NOT merge the token + creds into one JSON blob — the design rejects that too (would require migration of existing stored tokens).
- Do NOT add a mid-call browser fallback inside the retry shim — agents-in-background UX concern; a loud error is the design choice.
- Do NOT add a mutex around the retry path — the design explicitly accepts 1–2 duplicate refresh attempts under concurrency as a non-goal.
- The `.client` suffix relies on server names not containing dots in the config schema; do not change the config validator without re-checking this.
