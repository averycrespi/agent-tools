# OAuth Support for mcp-broker — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Enable mcp-broker to connect to OAuth-protected backend MCP servers (GitHub, Atlassian, Datadog) with keychain-backed token storage and inline browser auth.

**Architecture:** Add an `oauth` field to server config that triggers OAuth client constructors from mcp-go. Tokens stored in OS keychain via go-keyring. On first connect, if no token exists, open browser for PKCE auth flow with a local callback server, then retry the connection.

**Tech Stack:** mcp-go (OAuth client), go-keyring (keychain), Go stdlib (net/http for callback server, hash/fnv for port derivation)

---

### Task 1: Add OAuthConfig to config package

**Files:**
- Modify: `mcp-broker/internal/config/config.go:18-27`
- Test: `mcp-broker/internal/config/config_test.go`

**Step 1: Write the failing tests**

Add these tests to `mcp-broker/internal/config/config_test.go`:

```go
func TestConfig_OAuthTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "github", "type": "http", "url": "https://api.github.com/mcp", "oauth": true}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.NotNil(t, cfg.Servers[0].OAuth)
	require.Empty(t, cfg.Servers[0].OAuth.ClientID)
}

func TestConfig_OAuthObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "custom", "type": "http", "url": "https://mcp.example.com", "oauth": {
				"client_id": "my-app",
				"scopes": ["read", "write"],
				"auth_server_url": "https://auth.example.com"
			}}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.NotNil(t, cfg.Servers[0].OAuth)
	require.Equal(t, "my-app", cfg.Servers[0].OAuth.ClientID)
	require.Equal(t, []string{"read", "write"}, cfg.Servers[0].OAuth.Scopes)
	require.Equal(t, "https://auth.example.com", cfg.Servers[0].OAuth.AuthServerURL)
}

func TestConfig_OAuthAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": [
			{"name": "plain", "type": "http", "url": "https://example.com/mcp"}
		]
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 1)
	require.Nil(t, cfg.Servers[0].OAuth)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/config/ -run "TestConfig_OAuth" -v`
Expected: FAIL — `OAuthConfig` type doesn't exist, `OAuth` field doesn't exist on `ServerConfig`

**Step 3: Write the implementation**

In `mcp-broker/internal/config/config.go`, add the `OAuthConfig` struct and its custom unmarshaler above `ServerConfig`, then add the `OAuth` field to `ServerConfig`:

```go
// OAuthConfig holds OAuth settings for a backend server.
// Supports "oauth": true (all defaults) or "oauth": {...} with overrides.
type OAuthConfig struct {
	ClientID      string   `json:"client_id,omitempty"`
	ClientSecret  string   `json:"client_secret,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	AuthServerURL string   `json:"auth_server_url,omitempty"`
}

// UnmarshalJSON supports both "oauth": true and "oauth": {...}.
func (o *OAuthConfig) UnmarshalJSON(data []byte) error {
	if string(data) == "true" {
		return nil
	}
	type alias OAuthConfig
	return json.Unmarshal(data, (*alias)(o))
}
```

Add to `ServerConfig`:

```go
OAuth   *OAuthConfig      `json:"oauth,omitempty"`
```

**Step 4: Run tests to verify they pass**

Run: `cd mcp-broker && go test ./internal/config/ -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(mcp-broker): add OAuthConfig to server config"
```

---

### Task 2: Add go-keyring dependency and KeychainTokenStore

**Files:**
- Create: `mcp-broker/internal/server/oauth.go`
- Test: `mcp-broker/internal/server/oauth_test.go`

**Step 1: Add go-keyring dependency**

Run: `cd mcp-broker && go get github.com/zalando/go-keyring`

**Step 2: Write the failing tests**

Create `mcp-broker/internal/server/oauth_test.go`:

```go
package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func init() {
	keyring.MockInit()
}

func TestKeychainTokenStore_SaveAndGet(t *testing.T) {
	store := &KeychainTokenStore{serverName: "test-server"}
	ctx := context.Background()

	token := &transport.Token{
		AccessToken:  "access-123",
		TokenType:    "Bearer",
		RefreshToken: "refresh-456",
	}

	err := store.SaveToken(ctx, token)
	require.NoError(t, err)

	got, err := store.GetToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "access-123", got.AccessToken)
	require.Equal(t, "Bearer", got.TokenType)
	require.Equal(t, "refresh-456", got.RefreshToken)
}

func TestKeychainTokenStore_GetToken_NoToken(t *testing.T) {
	store := &KeychainTokenStore{serverName: "nonexistent-server"}
	ctx := context.Background()

	_, err := store.GetToken(ctx)
	require.ErrorIs(t, err, transport.ErrNoToken)
}

func TestCallbackPort_Deterministic(t *testing.T) {
	port1 := callbackPort("github")
	port2 := callbackPort("github")
	require.Equal(t, port1, port2)

	require.GreaterOrEqual(t, port1, 10000)
	require.LessOrEqual(t, port1, 65535)
}

func TestCallbackPort_DifferentServers(t *testing.T) {
	portGH := callbackPort("github")
	portAT := callbackPort("atlassian")
	require.NotEqual(t, portGH, portAT)
}
```

**Step 3: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/server/ -run "TestKeychainTokenStore|TestCallbackPort" -v`
Expected: FAIL — `KeychainTokenStore` and `callbackPort` don't exist

**Step 4: Write the implementation**

Create `mcp-broker/internal/server/oauth.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/zalando/go-keyring"
)

const keychainService = "mcp-broker"

// KeychainTokenStore implements transport.TokenStore using the OS keychain.
type KeychainTokenStore struct {
	serverName string
}

func (s *KeychainTokenStore) GetToken(ctx context.Context) (*transport.Token, error) {
	data, err := keyring.Get(keychainService, s.serverName)
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

func (s *KeychainTokenStore) SaveToken(ctx context.Context, token *transport.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	return keyring.Set(keychainService, s.serverName, string(data))
}

// callbackPort returns a deterministic port for the OAuth callback server,
// derived from the server name. Maps to ephemeral range 10000-65535.
func callbackPort(serverName string) int {
	h := fnv.New32a()
	h.Write([]byte(serverName))
	return int(h.Sum32()%(65535-10000)) + 10000
}
```

**Step 5: Run tests to verify they pass**

Run: `cd mcp-broker && go test ./internal/server/ -run "TestKeychainTokenStore|TestCallbackPort" -v`
Expected: ALL PASS

**Step 6: Commit**

```bash
cd mcp-broker && git add go.mod go.sum internal/server/oauth.go internal/server/oauth_test.go
git commit -m "feat(mcp-broker): add KeychainTokenStore and callbackPort"
```

---

### Task 3: Add OAuth connection flow and browser auth

**Files:**
- Modify: `mcp-broker/internal/server/oauth.go`
- Modify: `mcp-broker/internal/server/http.go:19,37`

**Step 1: Add the OAuth connection functions to oauth.go**

Append to `mcp-broker/internal/server/oauth.go`. This adds the browser auth flow, OAuth backend constructors, and the HTTP branching. The imports at the top of the file will need to be expanded.

Update the imports in `oauth.go` to:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zalando/go-keyring"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)
```

Then add these functions after `callbackPort`:

```go
// buildOAuthConfig creates an mcp-go OAuthConfig from our config.
func buildOAuthConfig(srv config.ServerConfig) transport.OAuthConfig {
	port := callbackPort(srv.Name)
	cfg := transport.OAuthConfig{
		RedirectURI: fmt.Sprintf("http://localhost:%d/callback", port),
		TokenStore:  &KeychainTokenStore{serverName: srv.Name},
		PKCEEnabled: true,
	}
	if srv.OAuth != nil {
		cfg.ClientID = srv.OAuth.ClientID
		cfg.ClientSecret = os.ExpandEnv(srv.OAuth.ClientSecret)
		cfg.Scopes = srv.OAuth.Scopes
		cfg.AuthServerMetadataURL = srv.OAuth.AuthServerURL
	}
	return cfg
}

// newOAuthHTTPBackend creates an HTTP backend with OAuth support.
func newOAuthHTTPBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := buildOAuthConfig(srv)

	var opts []transport.StreamableHTTPCOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	c, err := client.NewOAuthStreamableHttpClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create OAuth HTTP client for %q: %w", srv.Name, err)
	}

	if err := initializeOAuthClient(ctx, c, srv.Name); err != nil {
		return nil, err
	}

	return &httpBackend{client: c}, nil
}

// newOAuthSSEBackend creates an SSE backend with OAuth support.
func newOAuthSSEBackend(ctx context.Context, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := buildOAuthConfig(srv)

	var opts []transport.ClientOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}

	c, err := client.NewOAuthSSEClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create OAuth SSE client for %q: %w", srv.Name, err)
	}

	if err := c.Start(ctx); err != nil {
		if !client.IsOAuthAuthorizationRequiredError(err) {
			_ = c.Close()
			return nil, fmt.Errorf("start OAuth SSE client for %q: %w", srv.Name, err)
		}
		if err := runOAuthFlow(ctx, err); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("OAuth flow for %q: %w", srv.Name, err)
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("start OAuth SSE client for %q after auth: %w", srv.Name, err)
		}
	}

	if err := initializeOAuthClient(ctx, c, srv.Name); err != nil {
		return nil, err
	}

	return &httpBackend{client: c}, nil
}

// initializeOAuthClient sends the MCP Initialize handshake, handling OAuth auth if needed.
func initializeOAuthClient(ctx context.Context, c *client.Client, name string) error {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	_, err := c.Initialize(ctx, initReq)
	if err == nil {
		return nil
	}

	if !client.IsOAuthAuthorizationRequiredError(err) {
		_ = c.Close()
		return fmt.Errorf("initialize server %q: %w", name, err)
	}

	if err := runOAuthFlow(ctx, err); err != nil {
		_ = c.Close()
		return fmt.Errorf("OAuth flow for %q: %w", name, err)
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return fmt.Errorf("initialize server %q after auth: %w", name, err)
	}
	return nil
}

// runOAuthFlow runs the interactive browser-based OAuth flow.
// It extracts the OAuthHandler from the error, optionally performs dynamic
// client registration, starts a local callback server, opens the browser,
// waits for the callback, and exchanges the authorization code for a token.
func runOAuthFlow(ctx context.Context, authErr error) error {
	handler := client.GetOAuthHandler(authErr)
	if handler == nil {
		return fmt.Errorf("no OAuth handler in error")
	}

	// Dynamic client registration if no client ID
	if handler.GetClientID() == "" {
		if err := handler.RegisterClient(ctx, "mcp-broker"); err != nil {
			return fmt.Errorf("register client: %w", err)
		}
	}

	// Generate PKCE verifier and challenge
	codeVerifier, err := client.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := client.GenerateCodeChallenge(codeVerifier)

	// Generate state for CSRF protection
	state, err := client.GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	// Get the authorization URL
	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return fmt.Errorf("get authorization URL: %w", err)
	}

	// Start local callback server
	callbackCh := make(chan callbackResult, 1)
	srv, addr, err := startCallbackServer(callbackCh)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	defer srv.Close()

	// Open browser
	fmt.Fprintf(os.Stderr, "Opening browser for OAuth authentication...\n")
	fmt.Fprintf(os.Stderr, "If the browser doesn't open, visit: %s\n", authURL)
	openBrowser(authURL)

	// Wait for callback
	fmt.Fprintf(os.Stderr, "Waiting for authentication callback on %s...\n", addr)
	result := <-callbackCh

	if result.err != "" {
		return fmt.Errorf("OAuth callback error: %s", result.err)
	}

	// Exchange code for token
	if err := handler.ProcessAuthorizationResponse(ctx, result.code, state, codeVerifier); err != nil {
		return fmt.Errorf("exchange authorization code: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authentication successful!\n")
	return nil
}

type callbackResult struct {
	code string
	err  string
}

// startCallbackServer starts a local HTTP server to receive the OAuth callback.
// It listens on port 0 (OS-assigned) and returns the server, address, and any error.
// The caller should pass the redirect_uri port via callbackPort for deterministic ports,
// but this function uses the listener address for flexibility.
func startCallbackServer(ch chan<- callbackResult) (*http.Server, string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			ch <- callbackResult{err: errParam}
		} else {
			ch <- callbackResult{code: code}
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><h1>Authentication complete</h1><p>You can close this window.</p><script>window.close();</script></body></html>"))
	})

	srv := &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, "", fmt.Errorf("listen for callback: %w", err)
	}

	go srv.Serve(ln)

	return srv, ln.Addr().String(), nil
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	default:
		return
	}

	_ = exec.Command(cmd, args...).Start()
}
```

**Step 2: Wire up the OAuth branch in http.go**

In `mcp-broker/internal/server/http.go`, add OAuth branches at the top of both constructors.

At the beginning of `newHTTPBackend`, before `var opts`:

```go
if srv.OAuth != nil {
	return newOAuthHTTPBackend(ctx, srv)
}
```

At the beginning of `newSSEBackend`, before `var opts`:

```go
if srv.OAuth != nil {
	return newOAuthSSEBackend(ctx, srv)
}
```

**Step 3: Run all tests to verify nothing is broken**

Run: `cd mcp-broker && go test ./... -v`
Expected: ALL PASS (OAuth flow functions aren't unit-tested directly — they require a real OAuth server. The token store and port hash are already tested from Task 2.)

**Step 4: Run the linter**

Run: `cd mcp-broker && make fmt && make lint`
Expected: PASS (fix any issues reported)

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/server/oauth.go internal/server/http.go
git commit -m "feat(mcp-broker): add OAuth connection flow with browser auth"
```

---

### Task 4: Fix callback server to use deterministic port

**Files:**
- Modify: `mcp-broker/internal/server/oauth.go`

The `startCallbackServer` function currently listens on `:0` (random port), but the `buildOAuthConfig` function sets `RedirectURI` with a deterministic port from `callbackPort()`. These need to match — the callback server must listen on the same port that was registered as the redirect URI.

**Step 1: Write a test for the port match**

Add to `mcp-broker/internal/server/oauth_test.go`:

```go
func TestBuildOAuthConfig_RedirectURIMatchesCallbackPort(t *testing.T) {
	srv := config.ServerConfig{
		Name: "github",
		OAuth: &config.OAuthConfig{},
	}
	cfg := buildOAuthConfig(srv)

	port := callbackPort("github")
	expected := fmt.Sprintf("http://localhost:%d/callback", port)
	require.Equal(t, expected, cfg.RedirectURI)
}
```

Add the needed imports to the test file: `"fmt"` and `"github.com/averycrespi/agent-tools/mcp-broker/internal/config"`.

**Step 2: Run to verify it passes (buildOAuthConfig already uses callbackPort)**

Run: `cd mcp-broker && go test ./internal/server/ -run "TestBuildOAuthConfig" -v`
Expected: PASS

**Step 3: Update startCallbackServer to accept a port**

Change the `startCallbackServer` signature and implementation to use a specific port:

```go
func startCallbackServer(port int, ch chan<- callbackResult) (*http.Server, error) {
```

Change the listener line to:

```go
ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
```

Return signature becomes `(*http.Server, error)` (drop the `addr` return).

Update the caller in `runOAuthFlow` — extract the port from the handler's redirect URI or compute it from config. Since `runOAuthFlow` doesn't have access to the server name, we need to pass the port in. Change `runOAuthFlow` to accept the port:

```go
func runOAuthFlow(ctx context.Context, authErr error, port int) error {
```

And update the callback server call:

```go
srv, err := startCallbackServer(port, callbackCh)
```

And the log line:

```go
fmt.Fprintf(os.Stderr, "Waiting for authentication callback on localhost:%d...\n", port)
```

Update callers of `runOAuthFlow` in `initializeOAuthClient` and `newOAuthSSEBackend` to pass the port. Both functions have access to `srv.Name`, so compute `callbackPort(srv.Name)` and pass it.

In `newOAuthSSEBackend`:
```go
if err := runOAuthFlow(ctx, err, callbackPort(srv.Name)); err != nil {
```

In `initializeOAuthClient`, change the signature to accept the server name:
```go
func initializeOAuthClient(ctx context.Context, c *client.Client, name string) error {
```
(It already accepts `name`.) Then pass the port:
```go
if err := runOAuthFlow(ctx, err, callbackPort(name)); err != nil {
```

**Step 4: Run all tests**

Run: `cd mcp-broker && go test ./internal/server/ -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
cd mcp-broker && git add internal/server/oauth.go internal/server/oauth_test.go
git commit -m "fix(mcp-broker): use deterministic port for OAuth callback server"
```

---

### Task 5: Run full audit and fix any issues

**Files:**
- Possibly: `mcp-broker/internal/server/oauth.go` (lint fixes)
- Possibly: `mcp-broker/go.mod`, `mcp-broker/go.sum` (tidy)

**Step 1: Run go mod tidy**

Run: `cd mcp-broker && go mod tidy`
Expected: Clean output, no removed/added unexpected deps

**Step 2: Run full test suite**

Run: `cd mcp-broker && make test`
Expected: ALL PASS

**Step 3: Run linter**

Run: `cd mcp-broker && make fmt && make lint`
Expected: PASS — fix any issues flagged (common ones: unused imports, missing error checks on `w.Write`)

**Step 4: Commit any fixes**

```bash
cd mcp-broker && git add -A
git commit -m "chore(mcp-broker): lint and tidy for OAuth support"
```

(Skip this commit if no changes were needed.)

---

### Task 6: Update documentation

**Files:**
- Modify: `mcp-broker/CLAUDE.md`

**Step 1: Update the CLAUDE.md Architecture section**

In `mcp-broker/CLAUDE.md`, the `internal/server/` description currently says:

```
  server/               Backend interface with stdio, HTTP, and SSE transports
```

Update it to:

```
  server/               Backend interface with stdio, HTTP, SSE, and OAuth transports
```

**Step 2: Add an OAuth conventions entry**

In the `## Conventions` section, add:

```
- OAuth config supports `"oauth": true` (all defaults) or `"oauth": {...}` (with overrides) via custom `UnmarshalJSON`
- OAuth tokens are stored in the OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
```

**Step 3: Commit**

```bash
cd mcp-broker && git add CLAUDE.md
git commit -m "docs(mcp-broker): document OAuth support in CLAUDE.md"
```
