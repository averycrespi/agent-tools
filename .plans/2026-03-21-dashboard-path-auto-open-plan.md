# Dashboard Path Change + Auto-Open Browser — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Move the mcp-broker dashboard from `/` to `/dashboard` and auto-open the browser on startup.

**Architecture:** Two independent changes. (1) Re-mount the dashboard handler under `/dashboard/` using `http.StripPrefix`, update frontend fetch paths to relative URLs, and add a `/` → `/dashboard/` redirect. (2) Add `OpenBrowser` config field (default true) and `--no-open` CLI flag, then open the browser after the server starts listening.

**Tech Stack:** Go, Cobra CLI, `net/http`, `os/exec`, embedded HTML

---

### Task 1: Add `OpenBrowser` config field

**Files:**
- Modify: `mcp-broker/internal/config/config.go:10-16` (Config struct)
- Modify: `mcp-broker/internal/config/config.go:67-79` (DefaultConfig)
- Modify: `mcp-broker/internal/config/config_test.go`

**Step 1: Write the failing test**

Add to `mcp-broker/internal/config/config_test.go`:

```go
func TestDefaultConfig_OpenBrowserDefaultsTrue(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.OpenBrowser)
}

func TestLoad_OpenBrowserFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := os.WriteFile(path, []byte(`{"open_browser": false}`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.False(t, cfg.OpenBrowser)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/config/ -run 'TestDefaultConfig_OpenBrowser|TestLoad_OpenBrowser' -v`
Expected: FAIL — `cfg.OpenBrowser` is the zero value (false), and the field doesn't exist yet.

**Step 3: Add the field and default**

In `mcp-broker/internal/config/config.go`, add to the `Config` struct:

```go
type Config struct {
	Servers     []ServerConfig `json:"servers"`
	Rules       []RuleConfig   `json:"rules"`
	Port        int            `json:"port"`
	OpenBrowser bool           `json:"open_browser"`
	Audit       AuditConfig    `json:"audit"`
	Log         LogConfig      `json:"log"`
}
```

In `DefaultConfig()`, add `OpenBrowser: true`:

```go
func DefaultConfig() Config {
	return Config{
		Servers: []ServerConfig{},
		Rules: []RuleConfig{
			{Tool: "*", Verdict: "require-approval"},
		},
		Port:        8200,
		OpenBrowser: true,
		Audit: AuditConfig{
			Path: filepath.Join(xdgDataHome(), "mcp-broker", "audit.db"),
		},
		Log: LogConfig{Level: "info"},
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd mcp-broker && go test ./internal/config/ -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add mcp-broker/internal/config/config.go mcp-broker/internal/config/config_test.go
git commit -m "feat(mcp-broker): add OpenBrowser config field"
```

---

### Task 2: Move dashboard mount to `/dashboard`

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go:100-109`
- Modify: `mcp-broker/internal/dashboard/index.html` (6 fetch/EventSource calls)

**Step 1: Update serve.go mount points**

Replace the dashboard mount block (lines 100-109) with:

```go
// Create combined HTTP server
mux := http.NewServeMux()

// Mount MCP at /mcp
streamHandler := mcpserver.NewStreamableHTTPServer(mcpSrv)
mux.Handle("/mcp", streamHandler)

// Mount dashboard at /dashboard
dashHandler := dash.Handler()
mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashHandler))

// Redirect root to dashboard
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dashboard/", http.StatusFound)
})
```

**Step 2: Update index.html fetch paths to relative URLs**

Change all 6 occurrences. Each change removes the leading `/`:

| Line | Before | After |
|------|--------|-------|
| 861 | `fetch('/api/tools')` | `fetch('api/tools')` |
| 911 | `fetch('/api/decide',` | `fetch('api/decide',` |
| 933 | `fetch('/api/pending')` | `fetch('api/pending')` |
| 939 | `new EventSource('/events')` | `new EventSource('events')` |
| 960 | `fetch('/api/tools')` | `fetch('api/tools')` |
| 1093 | `fetch('/api/audit?'` | `fetch('api/audit?'` |

**Step 3: Run existing dashboard tests**

Run: `cd mcp-broker && go test ./internal/dashboard/ -v`
Expected: ALL PASS. The tests use `httptest.NewServer` which mounts `d.Handler()` at `/`, so internal routing is unaffected — `StripPrefix` only affects the outer mux in `serve.go`.

**Step 4: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go mcp-broker/internal/dashboard/index.html
git commit -m "feat(mcp-broker): move dashboard to /dashboard path"
```

---

### Task 3: Add `--no-open` flag and browser open logic

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`

**Step 1: Add the `--no-open` flag registration**

In the `init()` function in `serve.go`, add the flag:

```go
func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().String("log-level", "", "log level override (debug, info, warn, error)")
	serveCmd.Flags().Bool("no-open", false, "do not open dashboard in browser")
}
```

**Step 2: Add the `openBrowser` helper function**

Add at the bottom of `serve.go`:

```go
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
```

Add `"os/exec"` and `"runtime"` to the imports.

**Step 3: Add browser open logic after server starts**

In `runServe`, after the goroutine that calls `ListenAndServe`, add:

```go
go func() {
	logger.Info("listening", "addr", addr)
	errCh <- srv.ListenAndServe()
}()

// Open browser if enabled
noOpen, _ := cmd.Flags().GetBool("no-open")
if cfg.OpenBrowser && !noOpen {
	dashURL := fmt.Sprintf("http://localhost:%d/dashboard/", cfg.Port)
	logger.Debug("opening browser", "url", dashURL)
	if err := openBrowser(dashURL); err != nil {
		logger.Warn("failed to open browser", "error", err)
	}
}
```

**Step 4: Verify it compiles**

Run: `cd mcp-broker && go build ./cmd/mcp-broker`
Expected: Build succeeds.

**Step 5: Verify flag shows up in help**

Run: `cd mcp-broker && go run ./cmd/mcp-broker serve --help`
Expected: Output includes `--no-open` flag description.

**Step 6: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go
git commit -m "feat(mcp-broker): auto-open browser on startup"
```

---

### Task 4: Update documentation

**Files:**
- Modify: `mcp-broker/CLAUDE.md` — Update architecture line about dashboard path

**Step 1: Update CLAUDE.md**

The architecture section says:

> Single binary, single port. `/mcp` for agents, `/` for the web dashboard.

Change to:

> Single binary, single port. `/mcp` for agents, `/dashboard/` for the web dashboard.

**Step 2: Commit**

```bash
git add mcp-broker/CLAUDE.md
git commit -m "docs(mcp-broker): update dashboard path in CLAUDE.md"
```
