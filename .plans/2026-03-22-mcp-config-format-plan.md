# MCP Broker Config Format Redesign — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Change the `servers` section of the mcp-broker config from an array of objects to a map keyed by server name, remove explicit OAuth config, and rename `"http"` transport type to `"streamable-http"` — aligning with the format used by Claude Code and other MCP clients.

**Architecture:** Three layers change bottom-up: (1) config types and serialization, (2) server connection logic (manager + OAuth), (3) documentation. The OAuth runtime flow stays; only the config-driven setup is removed — all HTTP/SSE backends become OAuth-capable automatically.

**Tech Stack:** Go, JSON config, mcp-go library

**Design doc:** `.plans/2026-03-22-mcp-config-format-design.md`

---

### Task 1: Update config types and tests

**Files:**
- Modify: `mcp-broker/internal/config/config.go`
- Modify: `mcp-broker/internal/config/config_test.go`

**Step 1: Update the config structs**

Remove `OAuthConfig`, its `UnmarshalJSON`, and the `Name`/`OAuth` fields from `ServerConfig`. Change `Config.Servers` from slice to map. The `encoding/json` import stays (used by `Load`/`Save`).

Replace the entire type block at the top of `config.go` (lines 9–47) with:

```go
// Config is the top-level configuration for mcp-broker.
type Config struct {
	Servers     map[string]ServerConfig `json:"servers"`
	Rules       []RuleConfig            `json:"rules"`
	Port        int                     `json:"port"`
	OpenBrowser bool                    `json:"open_browser"`
	Audit       AuditConfig             `json:"audit"`
	Log         LogConfig               `json:"log"`
}

// ServerConfig defines a backend MCP server.
type ServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}
```

Also update `DefaultConfig()` — change `Servers: []ServerConfig{}` to `Servers: map[string]ServerConfig{}`.

**Step 2: Rewrite tests for new format**

Replace `TestConfig_ServerTypes` (lines 51–71) to use the map format:

```go
func TestConfig_ServerTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	data := `{
		"servers": {
			"echo": {"command": "echo", "args": ["hello"]},
			"remote": {"type": "streamable-http", "url": "http://localhost:3000/mcp"}
		}
	}`
	err := os.WriteFile(path, []byte(data), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Servers, 2)
	require.Equal(t, "echo", cfg.Servers["echo"].Command)
	require.Equal(t, "streamable-http", cfg.Servers["remote"].Type)
	require.Equal(t, "http://localhost:3000/mcp", cfg.Servers["remote"].URL)
}
```

Delete the three OAuth tests entirely: `TestConfig_OAuthTrue` (lines 90–107), `TestConfig_OAuthObject` (lines 109–132), and `TestConfig_OAuthAbsent` (lines 134–150).

**Step 3: Run tests to verify**

Run: `cd mcp-broker && go test ./internal/config/ -v -run 'TestConfig_ServerTypes|TestLoad|TestRefresh|TestDefaultConfig|TestConfigPath'`

Expected: All pass. The `TestLoad_*` and `TestRefresh_*` tests don't reference server fields so they should pass unchanged.

**Step 4: Commit**

```
git add mcp-broker/internal/config/config.go mcp-broker/internal/config/config_test.go
git commit -m "refactor(mcp-broker): change servers config from array to map

Remove OAuthConfig type and Name field from ServerConfig.
Servers are now keyed by name in a map, matching Claude Code format."
```

---

### Task 2: Update server manager to use map and new transport type

**Files:**
- Modify: `mcp-broker/internal/server/manager.go`
- Modify: `mcp-broker/internal/server/stdio.go`

**Step 1: Update `NewManager` signature and `connect` function**

In `manager.go`, change `NewManager` to accept a map and pass the name separately to `connect`:

```go
// NewManager creates a Manager and connects to all configured backends.
func NewManager(ctx context.Context, servers map[string]config.ServerConfig, logger *slog.Logger) (*Manager, error) {
	m := &Manager{
		backends: make(map[string]Backend),
		tools:    make(map[string]toolEntry),
		logger:   logger,
	}

	for name, srv := range servers {
		backend, err := connect(ctx, name, srv, logger)
		if err != nil {
			// Log and skip failed backends rather than failing entirely
			logger.Error("failed to connect to backend", "name", name, "error", err)
			continue
		}
		m.backends[name] = backend
		logger.Info("connected to backend", "name", name)
	}

	if err := m.discover(ctx); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return m, nil
}
```

Update `connect` to accept `name string` and use `"streamable-http"` instead of `"http"`:

```go
// connect creates a Backend for the given server config.
func connect(ctx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (Backend, error) {
	switch srv.Type {
	case "streamable-http":
		return newHTTPBackend(ctx, name, srv)
	case "sse":
		return newSSEBackend(ctx, name, srv)
	default:
		// stdio is the default
		return newStdioBackend(ctx, name, srv, logger)
	}
}
```

**Step 2: Update `newStdioBackend` signature in `stdio.go`**

Change `newStdioBackend` to accept `name string` separately and replace all `srv.Name` with `name`:

```go
func newStdioBackend(ctx context.Context, name string, srv config.ServerConfig, logger *slog.Logger) (*stdioBackend, error) {
	env := expandEnv(srv.Env)
	envSlice := os.Environ()
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(srv.Command, envSlice, srv.Args...)
	if err != nil {
		return nil, fmt.Errorf("spawn stdio server %q: %w", name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize stdio server %q: %w", name, err)
	}

	logger.Debug("stdio backend initialized", "name", name, "command", srv.Command)

	return &stdioBackend{client: c}, nil
}
```

**Step 3: Run unit tests**

Run: `cd mcp-broker && go test ./internal/server/ -v -run 'TestManager|TestExpandEnv'`

Expected: All pass. The manager tests construct `Manager` directly with a mock backend map, so they don't call `NewManager` or `connect` — they should be unaffected.

**Step 4: Commit**

```
git add mcp-broker/internal/server/manager.go mcp-broker/internal/server/stdio.go
git commit -m "refactor(mcp-broker): update manager and stdio to use map config

NewManager accepts map[string]ServerConfig. connect() and
newStdioBackend() take server name as a separate parameter.
Transport type 'http' renamed to 'streamable-http'."
```

---

### Task 3: Merge OAuth into HTTP/SSE backends

**Files:**
- Modify: `mcp-broker/internal/server/http.go`
- Modify: `mcp-broker/internal/server/oauth.go`
- Modify: `mcp-broker/internal/server/oauth_test.go`

**Step 1: Rewrite `http.go` to always use OAuth-capable clients**

Remove the `if srv.OAuth != nil` branching. All HTTP/SSE backends now use `client.NewOAuthStreamableHttpClient` / `client.NewOAuthSSEClient` with a minimal `transport.OAuthConfig`. Both functions take `name string` separately. Remove the `config` import since `ServerConfig` fields are accessed directly.

Replace `newHTTPBackend` and `newSSEBackend` (lines 19–66):

```go
func newHTTPBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := oauthConfig(name)

	var opts []transport.StreamableHTTPCOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}

	c, err := client.NewOAuthStreamableHttpClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %q: %w", name, err)
	}

	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
	}

	return &httpBackend{client: c}, nil
}

func newSSEBackend(ctx context.Context, name string, srv config.ServerConfig) (*httpBackend, error) {
	oauthCfg := oauthConfig(name)

	var opts []transport.ClientOption
	if headers := expandEnv(srv.Headers); len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}

	c, err := client.NewOAuthSSEClient(srv.URL, oauthCfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("create SSE client for %q: %w", name, err)
	}

	if err := c.Start(ctx); err != nil {
		if !client.IsOAuthAuthorizationRequiredError(err) {
			_ = c.Close()
			return nil, fmt.Errorf("start SSE client for %q: %w", name, err)
		}
		if err := runOAuthFlow(ctx, err, callbackPort(name)); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("OAuth flow for %q: %w", name, err)
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("start SSE client for %q after auth: %w", name, err)
		}
	}

	if err := initializeOAuthClient(ctx, c, name); err != nil {
		return nil, err
	}

	return &httpBackend{client: c}, nil
}
```

Also update `initializeClient` — delete it entirely, since `initializeOAuthClient` in `oauth.go` handles both OAuth and non-OAuth initialization. All callers now use `initializeOAuthClient`.

Remove the `config` import from `http.go` since `ServerConfig` is no longer referenced directly — use the fields via the function parameter. Actually, `srv config.ServerConfig` still references the type, so the import stays. Double-check at compile time.

**Step 2: Simplify `oauth.go`**

Replace `buildOAuthConfig` with a minimal `oauthConfig` function that takes only the server name:

```go
// oauthConfig creates a minimal OAuth config for automatic discovery.
// The mcp-go library handles 401 detection, metadata discovery, dynamic
// client registration, and PKCE automatically.
func oauthConfig(serverName string) transport.OAuthConfig {
	port := callbackPort(serverName)
	return transport.OAuthConfig{
		RedirectURI: fmt.Sprintf("http://localhost:%d/callback", port),
		TokenStore:  &KeychainTokenStore{serverName: serverName},
		PKCEEnabled: true,
	}
}
```

Delete `buildOAuthConfig`, `newOAuthHTTPBackend`, and `newOAuthSSEBackend` — their logic has been merged into `http.go`.

Remove the `config` and `os` imports from `oauth.go` (no longer needed after removing `buildOAuthConfig`).

**Step 3: Update `oauth_test.go`**

Delete `TestBuildOAuthConfig_RedirectURIMatchesCallbackPort` (the function no longer exists).

Add a replacement test for `oauthConfig`:

```go
func TestOAuthConfig_RedirectURIMatchesCallbackPort(t *testing.T) {
	cfg := oauthConfig("github")

	port := callbackPort("github")
	expected := fmt.Sprintf("http://localhost:%d/callback", port)
	require.Equal(t, expected, cfg.RedirectURI)
	require.True(t, cfg.PKCEEnabled)
	require.NotNil(t, cfg.TokenStore)
}
```

Remove the `config` import from `oauth_test.go`.

**Step 4: Verify everything compiles and tests pass**

Run: `cd mcp-broker && go build ./... && go test ./internal/server/ -v`

Expected: Compiles cleanly, all tests pass.

**Step 5: Commit**

```
git add mcp-broker/internal/server/http.go mcp-broker/internal/server/oauth.go mcp-broker/internal/server/oauth_test.go
git commit -m "refactor(mcp-broker): remove explicit OAuth config, always use OAuth-capable clients

All HTTP/SSE backends now use OAuth-capable mcp-go clients with
automatic 401 detection and discovery. Removes buildOAuthConfig,
newOAuthHTTPBackend, and newOAuthSSEBackend."
```

---

### Task 4: Update serve command caller

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Verify `serve.go` compiles**

The only reference to `cfg.Servers` is on line 87:

```go
mgr, err := server.NewManager(ctx, cfg.Servers, logger.With("component", "server"))
```

Since `cfg.Servers` is now `map[string]config.ServerConfig` and `NewManager` accepts that type, **no code changes are needed** in `serve.go`. But verify it compiles.

Run: `cd mcp-broker && go build ./cmd/mcp-broker/`

Expected: Compiles cleanly.

**Step 2: Run all unit tests**

Run: `cd mcp-broker && go test -race ./...`

Expected: All pass.

**Step 3: Commit (skip if no changes needed)**

No commit needed if `serve.go` required no changes — the compilation check is sufficient.

---

### Task 5: Update E2E tests

**Files:**
- Modify: `mcp-broker/test/e2e/teststack_test.go`

**Step 1: Update the test config types to use map format**

Change `testConfig.Servers` from `[]testServerConfig` to `map[string]testServerConfig`. Remove the `Name` field from `testServerConfig`.

Replace the test config types (lines 129–142):

```go
type testConfig struct {
	Servers     map[string]testServerConfig `json:"servers"`
	Rules       []testRuleConfig            `json:"rules"`
	Port        int                         `json:"port"`
	OpenBrowser bool                        `json:"open_browser"`
	Audit       testAuditConfig             `json:"audit"`
	Log         testLogConfig               `json:"log"`
}

type testServerConfig struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url"`
}
```

Update the config construction in `newTestStack` (lines 188–197). Change `"http"` to `"streamable-http"`:

```go
	cfg := testConfig{
		Servers: map[string]testServerConfig{
			"echo": {Type: "streamable-http", URL: backendURL},
		},
		Rules:       rules,
		Port:        brokerPort,
		OpenBrowser: false,
		Audit:       testAuditConfig{Path: filepath.Join(tmpDir, "audit.db")},
		Log:         testLogConfig{Level: "debug"},
	}
```

**Step 2: Run E2E tests**

Run: `cd mcp-broker && go test -race -tags=e2e -timeout=60s ./test/e2e/ -v`

Expected: All pass. The E2E tests test the full pipeline (mock backend → broker → MCP client), so this validates the entire config→connection flow works end-to-end.

**Step 3: Commit**

```
git add mcp-broker/test/e2e/teststack_test.go
git commit -m "test(mcp-broker): update E2E tests for map-based server config"
```

---

### Task 6: Update documentation

**Files:**
- Modify: `mcp-broker/README.md`
- Modify: `mcp-broker/CLAUDE.md`

**Step 1: Update `README.md` config example and servers table**

Replace the config example (lines 58–98) with:

```json
{
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {"GITHUB_TOKEN": "$GITHUB_TOKEN"}
    },
    "github-remote": {
      "type": "sse",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {"Authorization": "Bearer $GITHUB_TOKEN"}
    },
    "internal": {
      "type": "streamable-http",
      "url": "http://localhost:3000/mcp"
    }
  },
  "rules": [
    {"tool": "github.search_*", "verdict": "allow"},
    {"tool": "github.push*", "verdict": "require-approval"},
    {"tool": "*", "verdict": "require-approval"}
  ],
  "port": 8200,
  "audit": {
    "path": "~/.local/share/mcp-broker/audit.db"
  },
  "log": {
    "level": "info"
  }
}
```

Replace the servers table (lines 100–113) with:

```markdown
### Servers

Servers is a map keyed by server name. Each name is used as a tool prefix (e.g. `github.search`).

| Field | Description |
|-------|-------------|
| `command` | Command to spawn (stdio transport, default) |
| `args` | Command arguments |
| `env` | Environment variables; `$VAR` and `${VAR}` references are expanded from the process environment |
| `type` | Transport type: omit for stdio, `"streamable-http"` for Streamable HTTP, `"sse"` for SSE |
| `url` | URL for HTTP/SSE transport |
| `headers` | HTTP headers; `$VAR` and `${VAR}` references are expanded from the process environment |
```

Replace the OAuth section (lines 115–138) with:

```markdown
### OAuth

OAuth is handled automatically. When a server responds with HTTP 401, the broker runs an OAuth flow (dynamic client registration, PKCE, browser-based authorization). Tokens are stored in the OS keychain (macOS Keychain / Linux Secret Service) and refreshed automatically. No configuration is needed.
```

**Step 2: Update `CLAUDE.md`**

Replace the three OAuth convention lines (lines 49–51):

```
- OAuth config supports `"oauth": true` (all defaults) or `"oauth": {...}` (with overrides) via custom `UnmarshalJSON`
- OAuth tokens are stored in the OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
```

With:

```
- OAuth is auto-detected via 401 responses; tokens stored in OS keychain via `go-keyring` (service: `mcp-broker`, key: server name)
- OAuth callback port is deterministic per server name (FNV hash → ephemeral port range)
```

**Step 3: Commit**

```
git add mcp-broker/README.md mcp-broker/CLAUDE.md
git commit -m "docs(mcp-broker): update config docs for map-based server format

Remove OAuth config documentation, update examples to use map format
and 'streamable-http' transport type."
```
