# MCP Broker Authentication Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add bearer token authentication to all MCP broker HTTP endpoints.

**Architecture:** A single auth middleware wraps the HTTP mux, checking every request for a valid bearer token (via header or cookie). Token is auto-generated on first run and stored in a file with 0600 permissions. Dashboard gets a cookie-based flow for browser access.

**Tech Stack:** Go stdlib (`crypto/rand`, `crypto/subtle`, `net/http`, `encoding/hex`), Cobra (existing CLI framework), testify (existing test dep)

---

### Task 1: Token generation and loading

**Files:**
- Create: `mcp-broker/internal/auth/auth.go`
- Create: `mcp-broker/internal/auth/auth_test.go`

**Step 1: Write the failing tests**

```go
package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureToken_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64) // 32 bytes hex-encoded

	// File should exist with 0600 permissions.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// File contents should match returned token.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, token, string(data))
}

func TestEnsureToken_ReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-token")

	token1, err := EnsureToken(path)
	require.NoError(t, err)

	token2, err := EnsureToken(path)
	require.NoError(t, err)
	require.Equal(t, token1, token2)
}

func TestEnsureToken_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "auth-token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	require.Len(t, token, 64)
}

func TestLoadToken_FailsIfFileMissing(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nonexistent"))
	require.Error(t, err)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/auth/ -v -run 'TestEnsureToken|TestLoadToken'`
Expected: FAIL — package doesn't exist yet

**Step 3: Write minimal implementation**

```go
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// TokenPath returns the default token file path under the XDG config directory.
func TokenPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mcp-broker", "auth-token")
}

// EnsureToken loads the token from path, or generates and writes a new one if the file doesn't exist.
// Returns the 64-character hex token string.
func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	// Generate new token.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)

	// Write to file.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}
	return token, nil
}

// LoadToken reads the token from path. Returns an error if the file doesn't exist.
func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return string(data), nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd mcp-broker && go test ./internal/auth/ -v -run 'TestEnsureToken|TestLoadToken'`
Expected: PASS

**Step 5: Commit**

```bash
git add mcp-broker/internal/auth/auth.go mcp-broker/internal/auth/auth_test.go
git commit -m "feat(mcp-broker): add auth token generation and loading"
```

---

### Task 2: Auth middleware

**Files:**
- Modify: `mcp-broker/internal/auth/auth.go`
- Modify: `mcp-broker/internal/auth/auth_test.go`

**Step 1: Write the failing tests**

Append to `auth_test.go`:

```go
func TestMiddleware_AllowsValidBearerHeader(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_RejectsMissingAuth_MCP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mcp")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_RejectsInvalidToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMiddleware_AllowsValidCookie_Dashboard(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddleware_SetsTokenCookieAndRedirects(t *testing.T) {
	token := "abc123"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(token, inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Don't follow redirects.
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/dashboard/?token=" + token)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/", resp.Header.Get("Location"))

	// Should have set the cookie.
	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, cookieName, cookies[0].Name)
	require.Equal(t, token, cookies[0].Value)
	require.True(t, cookies[0].HttpOnly)
}

func TestMiddleware_RedirectsUnauthDashboard(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(srv.URL + "/dashboard/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/unauthorized", resp.Header.Get("Location"))
}

func TestMiddleware_AllowsUnauthorizedPage(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware("secret", inner)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dashboard/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
```

**Step 2: Run tests to verify they fail**

Run: `cd mcp-broker && go test ./internal/auth/ -v -run 'TestMiddleware'`
Expected: FAIL — `Middleware` and `cookieName` not defined

**Step 3: Write minimal implementation**

Add to `auth.go`:

```go
import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

const cookieName = "mcp-broker-auth"

// Middleware returns an HTTP handler that checks every request for a valid auth token.
// See the design doc for the full priority chain.
func Middleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 1. /dashboard/unauthorized is always allowed.
		if path == "/dashboard/unauthorized" {
			next.ServeHTTP(w, r)
			return
		}

		// 2. Check Authorization: Bearer <token> header.
		if checkBearer(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		// 3. Check cookie.
		if checkCookie(r, tokenBytes) {
			next.ServeHTTP(w, r)
			return
		}

		isDashboard := strings.HasPrefix(path, "/dashboard")

		// 4. Dashboard with ?token= query param: set cookie and redirect.
		if isDashboard {
			if qToken := r.URL.Query().Get("token"); qToken != "" {
				if subtle.ConstantTimeCompare([]byte(qToken), tokenBytes) == 1 {
					http.SetCookie(w, &http.Cookie{
						Name:     cookieName,
						Value:    token,
						Path:     "/dashboard/",
						HttpOnly: true,
						SameSite: http.SameSiteStrictMode,
						MaxAge:   int(365 * 24 * time.Hour / time.Second),
					})
					// Redirect to path without the token query param.
					clean := *r.URL
					q := clean.Query()
					q.Del("token")
					clean.RawQuery = q.Encode()
					http.Redirect(w, r, clean.Path, http.StatusFound)
					return
				}
			}

			// 5. Dashboard, no valid auth: redirect to unauthorized page.
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}

		// 6. Non-dashboard (i.e., /mcp): 401.
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}

func checkBearer(r *http.Request, token []byte) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	provided := []byte(strings.TrimPrefix(auth, "Bearer "))
	return subtle.ConstantTimeCompare(provided, token) == 1
}

func checkCookie(r *http.Request, token []byte) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), token) == 1
}
```

**Step 4: Run tests to verify they pass**

Run: `cd mcp-broker && go test ./internal/auth/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add mcp-broker/internal/auth/auth.go mcp-broker/internal/auth/auth_test.go
git commit -m "feat(mcp-broker): add auth middleware with bearer and cookie support"
```

---

### Task 3: Wire middleware into serve command

**Files:**
- Modify: `mcp-broker/cmd/mcp-broker/serve.go` (lines 54-140)

**Step 1: Import auth package and load token**

Add to the imports in `serve.go`:

```go
"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
```

In `runServe`, after config is loaded and logger is created (after line 67), add token loading:

```go
	// Load or generate auth token.
	tokenPath := auth.TokenPath()
	token, err := auth.EnsureToken(tokenPath)
	if err != nil {
		return fmt.Errorf("loading auth token: %w", err)
	}
	logger.Info("auth token loaded", "path", tokenPath)
```

**Step 2: Wrap mux with middleware**

Replace line 120:
```go
srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
```
with:
```go
srv := &http.Server{Addr: addr, Handler: auth.Middleware(token, mux), ReadHeaderTimeout: 10 * time.Second}
```

**Step 3: Update browser open URL to include token**

Replace lines 134-136:
```go
	if cfg.OpenBrowser && !noOpen {
		dashURL := fmt.Sprintf("http://localhost:%d/dashboard/", cfg.Port)
		logger.Debug("opening browser", "url", dashURL)
```
with:
```go
	if cfg.OpenBrowser && !noOpen {
		dashURL := fmt.Sprintf("http://localhost:%d/dashboard/?token=%s", cfg.Port, token)
		logger.Debug("opening browser")
```

**Step 4: Add dashboard URL log line**

After the `logger.Info("listening", ...)` line (line 128), add a log line printing the authenticated dashboard URL. Replace:

```go
		logger.Info("listening", "addr", addr)
```

with:

```go
		logger.Info("listening", "addr", addr)
		logger.Info("dashboard", "url", fmt.Sprintf("http://localhost:%d/dashboard/?token=%s", cfg.Port, token))
```

Wait — the token should not appear in logs. Instead, print the URL to stderr directly so the user can click it, but not via the structured logger:

```go
		logger.Info("listening", "addr", addr)
		fmt.Fprintf(os.Stderr, "Dashboard: http://localhost:%d/dashboard/?token=%s\n", cfg.Port, token)
```

**Step 5: Run unit tests**

Run: `cd mcp-broker && go test ./... -short`
Expected: PASS (compilation check — no runtime behavior change in unit tests)

**Step 6: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/serve.go
git commit -m "feat(mcp-broker): wire auth middleware into serve command"
```

---

### Task 4: Add unauthorized page to dashboard

**Files:**
- Modify: `mcp-broker/internal/dashboard/dashboard.go` (line 71-79)

**Step 1: Write the failing test**

Add to `mcp-broker/internal/dashboard/dashboard_test.go`:

```go
func TestDashboard_UnauthorizedPage(t *testing.T) {
	d := New(nil, nil, nil)
	mux := d.Handler()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "Unauthorized")
}
```

Add `"io"` to the test file imports.

**Step 2: Run test to verify it fails**

Run: `cd mcp-broker && go test ./internal/dashboard/ -v -run TestDashboard_UnauthorizedPage`
Expected: FAIL — the `/unauthorized` route returns the index page (or 404), not the unauthorized page

**Step 3: Write minimal implementation**

Add a new handler method to `dashboard.go`:

```go
func (d *Dashboard) handleUnauthorized(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Unauthorized - MCP Broker</title>
<style>body{font-family:system-ui,sans-serif;max-width:600px;margin:80px auto;padding:0 20px;color:#333}
h1{color:#c00}code{background:#f4f4f4;padding:2px 6px;border-radius:3px}</style>
</head><body>
<h1>Unauthorized</h1>
<p>You need to authenticate to access the MCP Broker dashboard.</p>
<p>Open the authenticated URL printed in the broker's startup output:</p>
<pre>Dashboard: http://localhost:PORT/dashboard/?token=TOKEN</pre>
<p>This sets a cookie so you won't need to do this again.</p>
</body></html>`)
}
```

Register the route in `Handler()` — add before the `GET /` catch-all:

```go
mux.HandleFunc("GET /unauthorized", d.handleUnauthorized)
```

**Step 4: Run test to verify it passes**

Run: `cd mcp-broker && go test ./internal/dashboard/ -v -run TestDashboard_UnauthorizedPage`
Expected: PASS

**Step 5: Commit**

```bash
git add mcp-broker/internal/dashboard/dashboard.go mcp-broker/internal/dashboard/dashboard_test.go
git commit -m "feat(mcp-broker): add unauthorized page to dashboard"
```

---

### Task 5: Add regen-token subcommand

**Files:**
- Create: `mcp-broker/cmd/mcp-broker/regentoken.go`

**Step 1: Write the subcommand**

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/auth"
)

func init() {
	rootCmd.AddCommand(regenTokenCmd)
}

var regenTokenCmd = &cobra.Command{
	Use:   "regen-token",
	Short: "Generate a new auth token (invalidates existing clients and dashboard sessions)",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := auth.TokenPath()

		// Delete existing token file so EnsureToken generates a new one.
		// Ignore error if file doesn't exist.
		_ = os.Remove(path)

		token, err := auth.EnsureToken(path)
		if err != nil {
			return fmt.Errorf("generating token: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "New token written to %s\n", path)
		fmt.Fprintf(cmd.OutOrStdout(), "Restart the broker to apply. Update client configs with the new token.\n")
		// Don't print the token itself — user can cat the file.
		_ = token
		return nil
	},
}
```

Add `"os"` to imports.

**Step 2: Build to verify compilation**

Run: `cd mcp-broker && go build ./cmd/mcp-broker`
Expected: SUCCESS

**Step 3: Manual smoke test**

Run: `cd mcp-broker && go run ./cmd/mcp-broker regen-token`
Expected: Prints "New token written to ..." and "Restart the broker to apply..."

**Step 4: Commit**

```bash
git add mcp-broker/cmd/mcp-broker/regentoken.go
git commit -m "feat(mcp-broker): add regen-token subcommand"
```

---

### Task 6: Update E2E tests for auth

**Files:**
- Modify: `mcp-broker/test/e2e/teststack_test.go` (lines 170-260 and 270-367)

The E2E tests start a real broker subprocess and make HTTP calls to it. With auth enabled, these calls will now get 401s. The test stack needs to:

1. Read the auto-generated token file from the broker's temp config directory
2. Pass the token as a Bearer header to MCP client connections
3. Pass the token as a Bearer header (or cookie) to dashboard API calls

**Step 1: Update teststack to read token after broker starts**

In `newTestStack`, after the broker is started and before the readiness poll, the token file will have been auto-generated at `<tmpDir>/mcp-broker/auth-token` (since XDG_CONFIG_HOME will be the config dir parent).

Actually — the broker uses `auth.TokenPath()` which reads `XDG_CONFIG_HOME`. We need to set `XDG_CONFIG_HOME` for the broker subprocess so the token lands in a known location. Add to the broker subprocess env:

In `newTestStack`, before `brokerCmd.Start()`, after line 207:

```go
	// Set XDG_CONFIG_HOME so the broker writes the auth token to a known location.
	brokerCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+tmpDir)
```

Then after the broker is ready (after line 236), read the token:

```go
	// Read the auto-generated auth token.
	tokenData, err := os.ReadFile(filepath.Join(tmpDir, "mcp-broker", "auth-token"))
	if err != nil {
		t.Fatalf("read auth token: %v", err)
	}
	authToken := string(tokenData)
```

**Step 2: Pass token to MCP client**

Update the MCP client creation (line 239) to include the Authorization header:

```go
	mcpClient, err := client.NewStreamableHttpClient(brokerURL+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer " + authToken,
	}))
```

**Step 3: Store token on TestStack**

Add `AuthToken` field to `TestStack` struct:

```go
type TestStack struct {
	BrokerURL string
	AuthToken string
	Client    *client.Client
	t         *testing.T
}
```

Set it in `newTestStack`:

```go
	return &TestStack{
		BrokerURL: brokerURL,
		AuthToken: authToken,
		Client:    mcpClient,
		t:         t,
	}
```

**Step 4: Add auth to dashboard API helpers**

Update `getPending`, `decide`, `getTools`, and `getAudit` to include the Bearer header. For each, replace `http.Get(...)` / `http.Post(...)` with a request that includes the header. For example, `getPending` becomes:

```go
func (s *TestStack) getPending() pendingResponse {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.BrokerURL+"/dashboard/api/pending", nil)
	req.Header.Set("Authorization", "Bearer "+s.AuthToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("get pending: %v", err)
	}
	defer resp.Body.Close()
	var items pendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		s.t.Fatalf("decode pending: %v", err)
	}
	return items
}
```

Apply the same pattern to `decide`, `getTools`, and `getAudit`.

**Step 5: Update readiness check to handle auth redirect**

The readiness poll (lines 221-236) currently checks `GET /dashboard/` expecting `200 OK`. With auth, this will now redirect to `/dashboard/unauthorized`. Update the poll to check for either 200 or check the unauthorized page instead:

```go
	// Wait for broker to be ready (poll the unauthenticated page).
	deadline := time.Now().Add(10 * time.Second)
	brokerReady := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(brokerURL + "/dashboard/unauthorized")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				brokerReady = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
```

**Step 6: Run E2E tests**

Run: `cd mcp-broker && go test -race -tags=e2e -timeout=60s ./test/e2e/...`
Expected: PASS

**Step 7: Commit**

```bash
git add mcp-broker/test/e2e/teststack_test.go
git commit -m "test(mcp-broker): update E2E tests for auth"
```

---

### Task 7: Run full audit

**Step 1: Run the full audit suite**

Run: `cd mcp-broker && make audit`

This runs: tidy, fmt, lint, test, govulncheck. Fix any issues that come up.

**Step 2: Commit any fixes**

```bash
git add -u mcp-broker/
git commit -m "chore(mcp-broker): fix lint and formatting issues from audit"
```

(Skip commit if no fixes needed.)

---

### Task 8: Update documentation

**Files:**
- Modify: `mcp-broker/CLAUDE.md`

**Step 1: Update architecture section**

Add `auth/` to the internal packages list in the Architecture section. After the `dashboard/` line, add:

```
  auth/                 Bearer token auth: generation, file storage, HTTP middleware
```

**Step 2: Add auth conventions**

Add to the Conventions section:

```
- Auth token file permissions: `0o600`, parent directories: `0o750`
- Auth token is 32 random bytes, hex-encoded (64 chars)
- Token comparison uses `crypto/subtle.ConstantTimeCompare`
- Dashboard auth uses `mcp-broker-auth` cookie (`HttpOnly`, `SameSite=Strict`)
```

**Step 3: Update the "No authentication" note**

The CLAUDE.md doesn't have an explicit "no authentication" note, but if there's a reference to "No authentication on dashboard" anywhere in docs, remove or update it.

**Step 4: Commit**

```bash
git add mcp-broker/CLAUDE.md
git commit -m "docs(mcp-broker): document auth architecture and conventions"
```
