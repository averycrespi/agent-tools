# Dashboard Path Change + Auto-Open Browser

## Summary

Move the mcp-broker dashboard from `/` to `/dashboard` and add an option to automatically open the browser on startup (default: true).

## Changes

### 1. Move dashboard to `/dashboard`

**serve.go** — Change the mount point and add a redirect:

```go
mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashHandler))
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    http.Redirect(w, r, "/dashboard/", http.StatusFound)
})
```

`StripPrefix` removes `/dashboard` before the request reaches the dashboard handler, so all internal routing (`/api/tools`, `/events`, etc.) continues to work unchanged.

A redirect from `/` to `/dashboard/` ensures users who visit the root URL land on the dashboard.

**index.html** — Change all fetch/EventSource paths from absolute to relative:

- `'/api/tools'` → `'api/tools'`
- `'/api/decide'` → `'api/decide'`
- `'/api/pending'` → `'api/pending'`
- `'/api/audit?...'` → `'api/audit?...'`
- `'/events'` → `'events'`

Since the page loads at `/dashboard/`, relative URLs resolve to `/dashboard/api/tools`, etc.

### 2. Auto-open browser (default: true)

**config.go** — Add field to `Config`:

```go
OpenBrowser bool `json:"open_browser"`
```

Set `OpenBrowser: true` in the `defaults()` function.

**serve.go** — After the HTTP server starts listening:

- Check config `OpenBrowser` and `--no-open` CLI flag
- If enabled, call `openBrowser(url)` which uses `exec.Command("open", url)` on macOS, `exec.Command("xdg-open", url)` on Linux
- Run in a goroutine so it doesn't block server startup
- Log the open attempt at debug level; log errors as warnings (non-fatal)

**CLI** — Add `--no-open` flag to the `serve` command. When set, it overrides the config value.

### 3. Files changed

| File | Change |
|------|--------|
| `cmd/mcp-broker/serve.go` | New mount path, `/` redirect, browser open logic, `--no-open` flag |
| `internal/config/config.go` | Add `OpenBrowser` field with default `true` |
| `internal/dashboard/index.html` | Remove leading `/` from all API/event paths |
