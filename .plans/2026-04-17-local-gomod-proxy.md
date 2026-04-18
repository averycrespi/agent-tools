# local-gomod-proxy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Build `local-gomod-proxy`, a host-side Go module proxy that lets sandboxed agents resolve public and private Go modules without holding the host's git credentials.

**Architecture:** Single-binary HTTP server. Implements the Go module proxy protocol. Reverse-proxies public modules to `proxy.golang.org`. For private modules (matching `GOPRIVATE`), shells out to the host's `go mod download` with the user's git credentials and serves the resulting artifacts from the host's `GOMODCACHE`. Auth via bearer token carried in the `GOPROXY` URL as HTTP Basic.

**Tech Stack:** Go 1.25, cobra (CLI), `golang.org/x/mod/module` (GOPRIVATE glob matching), `net/http/httputil.ReverseProxy`, stdlib `os/exec` for `go env` and `go mod download`, testify (tests).

**Design reference:** `.designs/2026-04-17-local-gomod-proxy.md`

---

## Conventions (apply to every task)

- Errors wrapped with context: `fmt.Errorf("doing X: %w", err)`; command output interpolated with `%s` after trimming.
- All external commands go through the `exec.Runner` interface. No direct `os/exec` in business logic.
- `cmd/` packages have no tests (thin wrappers). Every `internal/` package has tests.
- File permissions: `0o600` for files, `0o750` for directories.
- `gosec` `nolint` directives on `os/exec` calls are acceptable inside the `exec` package only.
- Tests use testify's `assert` / `require`. Prefer `require` for setup, `assert` for assertions.
- Integration tests gated with `//go:build integration`; E2E tests with `//go:build e2e`.
- Run `make audit` before every commit once the Makefile exists.
- Commit messages follow conventional commits: `feat(local-gomod-proxy): ...`, `test(local-gomod-proxy): ...`, etc.

---

## Task 1: Scaffold the module

**Files:**

- Create: `local-gomod-proxy/go.mod`
- Create: `local-gomod-proxy/Makefile`
- Create: `local-gomod-proxy/cmd/local-gomod-proxy/main.go`
- Create: `local-gomod-proxy/cmd/local-gomod-proxy/root.go`
- Modify: `go.work`
- Modify: `Makefile` (root)

**Step 1: Initialize the module**

From the repo root:

```bash
mkdir -p local-gomod-proxy/cmd/local-gomod-proxy
cd local-gomod-proxy
go mod init github.com/averycrespi/agent-tools/local-gomod-proxy
```

**Step 2: Create the Makefile**

`local-gomod-proxy/Makefile` (identical pattern to `local-git-mcp/Makefile` with an extra `test-e2e` target):

```makefile
.PHONY: build install test test-integration test-e2e lint fmt tidy audit

build:
	go build -o local-gomod-proxy ./cmd/local-gomod-proxy

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/local-gomod-proxy

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-e2e:
	go test -race -tags=e2e -timeout=60s ./test/e2e/...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
```

**Step 3: Create the cobra skeleton**

`local-gomod-proxy/cmd/local-gomod-proxy/main.go`:

```go
package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
```

`local-gomod-proxy/cmd/local-gomod-proxy/root.go`:

```go
package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:          "local-gomod-proxy",
	Short:        "Host-side Go module proxy for sandboxed agents",
	SilenceUsage: true,
}
```

**Step 4: Add to go.work and root Makefile**

Modify `go.work` — insert `./local-gomod-proxy` in the `use (...)` block, keeping entries sorted.

Modify root `Makefile` line 1 — append `local-gomod-proxy` to the `TOOLS :=` list.

**Step 5: Add cobra dep and verify it builds**

```bash
cd local-gomod-proxy
go get github.com/spf13/cobra@latest
go mod tidy
make build
```

Expected: binary `local-gomod-proxy` produced, no errors.

**Step 6: Commit**

```bash
git add local-gomod-proxy go.work Makefile
git commit -m "feat(local-gomod-proxy): scaffold module with cobra CLI"
```

---

## Task 2: exec.Runner interface

**Files:**

- Create: `local-gomod-proxy/internal/exec/exec.go`
- Create: `local-gomod-proxy/internal/exec/exec_test.go`

This package is copied verbatim from `local-git-mcp/internal/exec/exec.go`. Same interface, same package name, so the rest of the code is portable across siblings.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/exec/exec_test.go`:

```go
package exec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSRunner_Run_Success(t *testing.T) {
	r := NewOSRunner()
	out, err := r.Run("echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestOSRunner_Run_Error(t *testing.T) {
	r := NewOSRunner()
	_, err := r.Run("false")
	assert.Error(t, err)
}

func TestOSRunner_RunDir_UsesDir(t *testing.T) {
	r := NewOSRunner()
	dir := t.TempDir()
	out, err := r.RunDir(dir, "pwd")
	require.NoError(t, err)
	assert.Contains(t, string(out), dir)
}
```

**Step 2: Run the test, confirm failure**

```bash
cd local-gomod-proxy && go test ./internal/exec/...
```

Expected: compilation error (package doesn't exist yet).

**Step 3: Implement**

`local-gomod-proxy/internal/exec/exec.go`:

```go
package exec

import (
	osexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
	RunDir(dir, name string, args ...string) ([]byte, error)
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	cmd := osexec.Command(name, args...) //nolint:gosec
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
```

**Step 4: Add testify dep, run tests**

```bash
go get github.com/stretchr/testify
go mod tidy
make test
```

Expected: all 3 tests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/exec local-gomod-proxy/go.mod local-gomod-proxy/go.sum
git commit -m "feat(local-gomod-proxy): add exec.Runner interface"
```

---

## Task 3: auth package — token generation and loading

**Files:**

- Create: `local-gomod-proxy/internal/auth/token.go`
- Create: `local-gomod-proxy/internal/auth/token_test.go`

Adapted from `mcp-broker/internal/auth/auth.go` but scoped to this tool (different XDG subdir). Keep only `TokenPath`, `EnsureToken`, `LoadToken`. Middleware comes in Task 4.

**Step 1: Write the failing tests**

`local-gomod-proxy/internal/auth/token_test.go`:

```go
package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenPath_UsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	assert.Equal(t, "/tmp/xdg/local-gomod-proxy/auth-token", TokenPath())
}

func TestEnsureToken_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "token")

	token, err := EnsureToken(path)
	require.NoError(t, err)
	assert.Len(t, token, 64) // 32 bytes hex-encoded

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEnsureToken_ReusesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("deadbeef"), 0o600))

	token, err := EnsureToken(path)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", token)
}

func TestLoadToken_ErrorsWhenMissing(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nope"))
	assert.Error(t, err)
}
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/auth/...
```

Expected: package doesn't exist.

**Step 3: Implement**

`local-gomod-proxy/internal/auth/token.go`:

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
	return filepath.Join(base, "local-gomod-proxy", "auth-token")
}

// EnsureToken loads the token from path, or generates and writes a new one
// if the file doesn't exist. Returns the 64-character hex token.
func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)

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

**Step 4: Run tests**

```bash
make test
```

Expected: all 4 tests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/auth
git commit -m "feat(local-gomod-proxy): add auth token storage"
```

---

## Task 4: auth middleware — HTTP Basic and Bearer

**Files:**

- Create: `local-gomod-proxy/internal/auth/middleware.go`
- Create: `local-gomod-proxy/internal/auth/middleware_test.go`

Accept both HTTP Basic (`Authorization: Basic base64(user:token)`) and Bearer (`Authorization: Bearer <token>`). The Go module tooling sends Basic when the token is embedded in the `GOPROXY` URL; Bearer support is handy for `curl` debugging. Username portion of Basic is ignored (Go sends `_` by convention).

**Step 1: Write the failing tests**

`local-gomod-proxy/internal/auth/middleware_test.go`:

```go
package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newReq(auth string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/foo/@v/list", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestMiddleware_BearerValid(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq("Bearer secret"))
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestMiddleware_BasicValid(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("_:secret"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(basic))
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestMiddleware_Missing(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(""))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_WrongToken(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq("Bearer wrong"))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
```

**Step 2: Run tests, confirm failure**

```bash
go test ./internal/auth/...
```

Expected: undefined `Middleware`.

**Step 3: Implement**

`local-gomod-proxy/internal/auth/middleware.go`:

```go
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Middleware enforces bearer-token auth. Accepts either:
//   - Authorization: Bearer <token>
//   - Authorization: Basic <base64(user:token)>  (user portion is ignored)
func Middleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := extractToken(r)
		if !ok || subtle.ConstantTimeCompare(provided, tokenBytes) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractToken(r *http.Request) ([]byte, bool) {
	h := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(h, "Bearer "):
		return []byte(strings.TrimPrefix(h, "Bearer ")), true
	case strings.HasPrefix(h, "Basic "):
		if _, pw, ok := r.BasicAuth(); ok {
			return []byte(pw), true
		}
	}
	return nil, false
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: all 8 auth tests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/auth
git commit -m "feat(local-gomod-proxy): add HTTP basic/bearer auth middleware"
```

---

## Task 5: goenv package — wrap `go env -json`

**Files:**

- Create: `local-gomod-proxy/internal/goenv/goenv.go`
- Create: `local-gomod-proxy/internal/goenv/goenv_test.go`

Reads `GOPRIVATE`, `GOMODCACHE`, `GOVERSION` via `go env -json`. Returns a typed struct. Use `exec.Runner` so tests can stub the output.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/goenv/goenv_test.go`:

```go
package goenv

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
	err error
}

func (s stubRunner) Run(_ string, _ ...string) ([]byte, error)     { return s.out, s.err }
func (s stubRunner) RunDir(_ string, _ string, _ ...string) ([]byte, error) { return s.out, s.err }

func TestRead_ParsesJSON(t *testing.T) {
	runner := stubRunner{out: []byte(`{"GOPRIVATE":"github.com/foo/*","GOMODCACHE":"/home/x/pkg/mod","GOVERSION":"go1.25.8"}`)}

	env, err := Read(runner)
	require.NoError(t, err)
	assert.Equal(t, "github.com/foo/*", env.GOPRIVATE)
	assert.Equal(t, "/home/x/pkg/mod", env.GOMODCACHE)
	assert.Equal(t, "go1.25.8", env.GOVERSION)
}

func TestRead_PropagatesError(t *testing.T) {
	runner := stubRunner{err: errors.New("boom")}
	_, err := Read(runner)
	assert.Error(t, err)
}
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/goenv/...
```

Expected: package doesn't exist.

**Step 3: Implement**

`local-gomod-proxy/internal/goenv/goenv.go`:

```go
package goenv

import (
	"encoding/json"
	"fmt"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
)

// Env holds the subset of go env the proxy cares about.
type Env struct {
	GOPRIVATE  string `json:"GOPRIVATE"`
	GOMODCACHE string `json:"GOMODCACHE"`
	GOVERSION  string `json:"GOVERSION"`
}

// Read shells out to `go env -json` and parses the result.
func Read(runner exec.Runner) (Env, error) {
	out, err := runner.Run("go", "env", "-json", "GOPRIVATE", "GOMODCACHE", "GOVERSION")
	if err != nil {
		return Env{}, fmt.Errorf("running go env: %w: %s", err, out)
	}
	var env Env
	if err := json.Unmarshal(out, &env); err != nil {
		return Env{}, fmt.Errorf("parsing go env output: %w", err)
	}
	return env, nil
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: both tests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/goenv
git commit -m "feat(local-gomod-proxy): read go env via stubbable runner"
```

---

## Task 6: router — GOPRIVATE pattern matching

**Files:**

- Create: `local-gomod-proxy/internal/router/router.go`
- Create: `local-gomod-proxy/internal/router/router_test.go`

Router answers one question: "is this module path private?" Delegates to `golang.org/x/mod/module.MatchPrefixPatterns`, the exact function Go's toolchain uses.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/router/router_test.go`:

```go
package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPrivate(t *testing.T) {
	tests := []struct {
		name     string
		patterns string
		module   string
		want     bool
	}{
		{"exact match", "github.com/foo/bar", "github.com/foo/bar", true},
		{"wildcard match", "github.com/foo/*", "github.com/foo/bar", true},
		{"subpath of wildcard", "github.com/foo/*", "github.com/foo/bar/baz", true},
		{"no match", "github.com/foo/*", "github.com/other/repo", false},
		{"empty patterns", "", "github.com/any/thing", false},
		{"comma-separated", "github.com/a/*,github.com/b/*", "github.com/b/x", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.patterns)
			assert.Equal(t, tc.want, r.IsPrivate(tc.module))
		})
	}
}
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/router/...
```

Expected: package doesn't exist.

**Step 3: Implement**

`local-gomod-proxy/internal/router/router.go`:

```go
package router

import "golang.org/x/mod/module"

// Router classifies module paths as private or public based on GOPRIVATE-style
// glob patterns.
type Router struct {
	patterns string
}

// New returns a Router for the given GOPRIVATE value (comma-separated globs).
func New(patterns string) *Router {
	return &Router{patterns: patterns}
}

// IsPrivate reports whether the module path matches any configured pattern.
func (r *Router) IsPrivate(modulePath string) bool {
	if r.patterns == "" {
		return false
	}
	return module.MatchPrefixPatterns(r.patterns, modulePath)
}
```

**Step 4: Pull dep and run tests**

```bash
go get golang.org/x/mod
go mod tidy
make test
```

Expected: all router subtests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/router local-gomod-proxy/go.mod local-gomod-proxy/go.sum
git commit -m "feat(local-gomod-proxy): add GOPRIVATE-based router"
```

---

## Task 7: public fetcher — reverse-proxy to proxy.golang.org

**Files:**

- Create: `local-gomod-proxy/internal/public/public.go`
- Create: `local-gomod-proxy/internal/public/public_test.go`

Wraps `httputil.ReverseProxy` pointing at `https://proxy.golang.org`. The proxy rewrites the host and strips our inbound `Authorization` header so the upstream never sees it.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/public/public_test.go`:

```go
package public

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetcher_ForwardsToUpstream(t *testing.T) {
	var gotPath string
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Version":"v1.0.0"}`)
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	f := New(u)

	req := httptest.NewRequest(http.MethodGet, "/rsc.io/quote/@v/list", nil)
	req.Header.Set("Authorization", "Bearer super-secret")
	w := httptest.NewRecorder()
	f.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/rsc.io/quote/@v/list", gotPath)
	assert.Empty(t, gotAuth, "upstream must not see the inbound token")
	assert.Contains(t, w.Body.String(), "v1.0.0")
}
```

**Step 2: Run the test, confirm failure**

```bash
go test ./internal/public/...
```

Expected: package doesn't exist.

**Step 3: Implement**

`local-gomod-proxy/internal/public/public.go`:

```go
package public

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Fetcher reverse-proxies public module requests to a Go module proxy
// (typically proxy.golang.org).
type Fetcher struct {
	proxy *httputil.ReverseProxy
}

// New returns a Fetcher targeting the given upstream URL.
func New(upstream *url.URL) *Fetcher {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		orig(r)
		// Never leak our inbound auth to the upstream.
		r.Header.Del("Authorization")
		// Ensure Host matches the upstream.
		r.Host = upstream.Host
	}
	return &Fetcher{proxy: rp}
}

// ServeHTTP implements http.Handler.
func (f *Fetcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.proxy.ServeHTTP(w, r)
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: public test passes.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/public
git commit -m "feat(local-gomod-proxy): reverse-proxy public modules upstream"
```

---

## Task 8: private fetcher — parse module path and run `go mod download`

**Files:**

- Create: `local-gomod-proxy/internal/private/parse.go`
- Create: `local-gomod-proxy/internal/private/parse_test.go`

Before any subprocess runs, we parse the incoming proxy URL path into a `(module, version, artifact)` tuple and reject anything malformed. Keeping this in its own file makes it trivially unit-testable. Security-critical: this is the only place we decide what to feed to `go mod download`.

The Go module proxy protocol uses a case-escaping scheme (uppercase letters become `!x`). We'll use `golang.org/x/mod/module.UnescapePath` / `UnescapeVersion`.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/private/parse_test.go`:

```go
package private

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantMod    string
		wantVer    string
		wantArt    Artifact
		wantErr    bool
	}{
		{"info", "/github.com/foo/bar/@v/v1.2.3.info", "github.com/foo/bar", "v1.2.3", ArtifactInfo, false},
		{"mod", "/github.com/foo/bar/@v/v1.2.3.mod", "github.com/foo/bar", "v1.2.3", ArtifactMod, false},
		{"zip", "/github.com/foo/bar/@v/v1.2.3.zip", "github.com/foo/bar", "v1.2.3", ArtifactZip, false},
		{"list", "/github.com/foo/bar/@v/list", "github.com/foo/bar", "", ArtifactList, false},
		{"latest", "/github.com/foo/bar/@latest", "github.com/foo/bar", "", ArtifactLatest, false},
		{"escaped uppercase", "/github.com/!foo/bar/@v/v1.0.0.info", "github.com/Foo/bar", "v1.0.0", ArtifactInfo, false},
		{"bad path", "/not-a-module", "", "", 0, true},
		{"bad artifact", "/github.com/foo/bar/@v/v1.0.0.tar", "", "", 0, true},
		{"traversal attempt", "/../@v/list", "", "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseRequest(tc.path)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantMod, req.Module)
			assert.Equal(t, tc.wantVer, req.Version)
			assert.Equal(t, tc.wantArt, req.Artifact)
		})
	}
}
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/private/...
```

Expected: package doesn't exist.

**Step 3: Implement**

`local-gomod-proxy/internal/private/parse.go`:

```go
package private

import (
	"fmt"
	"strings"

	"golang.org/x/mod/module"
)

// Artifact identifies which file the client is requesting.
type Artifact int

const (
	_ Artifact = iota
	ArtifactInfo
	ArtifactMod
	ArtifactZip
	ArtifactList
	ArtifactLatest
)

// Request is a parsed Go module proxy request.
type Request struct {
	Module   string
	Version  string // empty for List and Latest
	Artifact Artifact
}

// ParseRequest parses a proxy URL path (with leading slash) into a Request.
// Rejects malformed paths, unsupported artifacts, and path traversal.
func ParseRequest(path string) (Request, error) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" || strings.Contains(trimmed, "..") {
		return Request{}, fmt.Errorf("invalid path: %q", path)
	}

	// /@latest form
	if idx := strings.LastIndex(trimmed, "/@latest"); idx >= 0 && idx == len(trimmed)-len("/@latest") {
		modEsc := trimmed[:idx]
		mod, err := module.UnescapePath(modEsc)
		if err != nil {
			return Request{}, fmt.Errorf("invalid module path: %w", err)
		}
		return Request{Module: mod, Artifact: ArtifactLatest}, nil
	}

	// /@v/... form
	idx := strings.Index(trimmed, "/@v/")
	if idx < 0 {
		return Request{}, fmt.Errorf("path missing /@v/ or /@latest: %q", path)
	}
	modEsc := trimmed[:idx]
	rest := trimmed[idx+len("/@v/"):]

	mod, err := module.UnescapePath(modEsc)
	if err != nil {
		return Request{}, fmt.Errorf("invalid module path: %w", err)
	}

	if rest == "list" {
		return Request{Module: mod, Artifact: ArtifactList}, nil
	}

	// <version>.<ext>
	dot := strings.LastIndex(rest, ".")
	if dot < 0 {
		return Request{}, fmt.Errorf("invalid artifact: %q", rest)
	}
	verEsc, ext := rest[:dot], rest[dot+1:]
	ver, err := module.UnescapeVersion(verEsc)
	if err != nil {
		return Request{}, fmt.Errorf("invalid version: %w", err)
	}

	var art Artifact
	switch ext {
	case "info":
		art = ArtifactInfo
	case "mod":
		art = ArtifactMod
	case "zip":
		art = ArtifactZip
	default:
		return Request{}, fmt.Errorf("unsupported artifact extension: %q", ext)
	}
	return Request{Module: mod, Version: ver, Artifact: art}, nil
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: all parse subtests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/private
git commit -m "feat(local-gomod-proxy): parse proxy protocol URLs"
```

---

## Task 9: private fetcher — serve artifacts via `go mod download`

**Files:**

- Create: `local-gomod-proxy/internal/private/fetcher.go`
- Create: `local-gomod-proxy/internal/private/fetcher_test.go`

Given a parsed `Request`, invoke `go mod download -json <module>@<version>` with the host's environment (so it picks up git creds and GOPRIVATE). Parse the JSON result for `Info`, `GoMod`, `Zip` file paths. Stream the requested file back. For `ArtifactList` invoke `go list -m -json -versions <module>@latest`. For `ArtifactLatest` invoke `go list -m -json <module>@latest`.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/private/fetcher_test.go`:

```go
package private

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
	err error
	got struct {
		dir  string
		name string
		args []string
	}
}

func (s *stubRunner) Run(name string, args ...string) ([]byte, error) {
	s.got.name, s.got.args = name, args
	return s.out, s.err
}

func (s *stubRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	s.got.dir, s.got.name, s.got.args = dir, name, args
	return s.out, s.err
}

func TestFetcher_Info_StreamsFile(t *testing.T) {
	// Arrange: write a fake .info file that go mod download would have produced.
	tmp := t.TempDir()
	infoPath := filepath.Join(tmp, "v1.2.3.info")
	require.NoError(t, os.WriteFile(infoPath, []byte(`{"Version":"v1.2.3"}`), 0o600))

	runner := &stubRunner{
		out: []byte(`{"Info":"` + infoPath + `","GoMod":"/x","Zip":"/y","Version":"v1.2.3"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Version":"v1.2.3"`)
	assert.Equal(t, "go", runner.got.name)
	assert.Equal(t, []string{"mod", "download", "-json", "github.com/foo/bar@v1.2.3"}, runner.got.args)
}

func TestFetcher_List_ReturnsPlaintext(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Path":"github.com/foo/bar","Versions":["v1.0.0","v1.1.0"]}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactList}
	w := httptest.NewRecorder()
	require.NoError(t, f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "v1.0.0\nv1.1.0\n", w.Body.String())
	assert.Equal(t, []string{"list", "-m", "-json", "-versions", "github.com/foo/bar@latest"}, runner.got.args)
}

func TestFetcher_Latest_ReturnsInfoJSON(t *testing.T) {
	runner := &stubRunner{
		out: []byte(`{"Path":"github.com/foo/bar","Version":"v1.1.0","Time":"2024-01-01T00:00:00Z"}`),
	}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Artifact: ArtifactLatest}
	w := httptest.NewRecorder()
	require.NoError(t, f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Version":"v1.1.0"`)
}

func TestFetcher_PropagatesToolError(t *testing.T) {
	runner := &stubRunner{err: assertErr{}, out: []byte("go: no such module")}
	f := New(runner)

	req := Request{Module: "github.com/foo/bar", Version: "v1.2.3", Artifact: ArtifactInfo}
	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	assert.Error(t, err)
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/private/...
```

Expected: undefined `New`, `Fetcher`, etc.

**Step 3: Implement**

`local-gomod-proxy/internal/private/fetcher.go`:

```go
package private

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
)

// Fetcher serves private module artifacts by invoking the host's go toolchain.
type Fetcher struct {
	runner exec.Runner
}

// New returns a Fetcher that shells out via runner.
func New(runner exec.Runner) *Fetcher {
	return &Fetcher{runner: runner}
}

type downloadResult struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Info    string `json:"Info"`
	GoMod   string `json:"GoMod"`
	Zip     string `json:"Zip"`
	Error   string `json:"Error"`
}

type listResult struct {
	Path     string   `json:"Path"`
	Version  string   `json:"Version"`
	Time     string   `json:"Time"`
	Versions []string `json:"Versions"`
	Error    string   `json:"Error"`
}

// Serve handles a single Request.
func (f *Fetcher) Serve(w http.ResponseWriter, _ *http.Request, req Request) error {
	switch req.Artifact {
	case ArtifactInfo, ArtifactMod, ArtifactZip:
		return f.serveArtifact(w, req)
	case ArtifactList:
		return f.serveList(w, req)
	case ArtifactLatest:
		return f.serveLatest(w, req)
	default:
		return fmt.Errorf("unsupported artifact: %d", req.Artifact)
	}
}

func (f *Fetcher) serveArtifact(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "mod", "download", "-json", req.Module+"@"+req.Version)
	if err != nil {
		return fmt.Errorf("go mod download: %w: %s", err, out)
	}
	var r downloadResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go mod download output: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("go mod download reported: %s", r.Error)
	}
	var path, contentType string
	switch req.Artifact {
	case ArtifactInfo:
		path, contentType = r.Info, "application/json"
	case ArtifactMod:
		path, contentType = r.GoMod, "text/plain; charset=utf-8"
	case ArtifactZip:
		path, contentType = r.Zip, "application/zip"
	}
	return streamFile(w, path, contentType)
}

func (f *Fetcher) serveList(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "list", "-m", "-json", "-versions", req.Module+"@latest")
	if err != nil {
		return fmt.Errorf("go list: %w: %s", err, out)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, strings.Join(r.Versions, "\n"))
	if len(r.Versions) > 0 {
		_, _ = io.WriteString(w, "\n")
	}
	return nil
}

func (f *Fetcher) serveLatest(w http.ResponseWriter, req Request) error {
	out, err := f.runner.Run("go", "list", "-m", "-json", req.Module+"@latest")
	if err != nil {
		return fmt.Errorf("go list: %w: %s", err, out)
	}
	var r listResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parsing go list output: %w", err)
	}
	info := map[string]string{"Version": r.Version, "Time": r.Time}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(info)
}

func streamFile(w http.ResponseWriter, path, contentType string) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, f)
	return err
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: all 4 fetcher tests + all 9 parse subtests pass.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/private
git commit -m "feat(local-gomod-proxy): resolve private modules via go mod download"
```

---

## Task 10: server — wire router, fetchers, auth into an `http.Handler`

**Files:**

- Create: `local-gomod-proxy/internal/server/server.go`
- Create: `local-gomod-proxy/internal/server/server_test.go`

The server's single HTTP handler:

1. Parse the request (proxy protocol).
2. Ask the router — private or public?
3. Public → delegate to `public.Fetcher`.
4. Private → delegate to `private.Fetcher`.
5. Log result.

Auth middleware wraps the whole thing.

**Step 1: Write the failing test**

`local-gomod-proxy/internal/server/server_test.go`:

```go
package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct {
	out []byte
}

func (s *stubRunner) Run(_ string, _ ...string) ([]byte, error)                  { return s.out, nil }
func (s *stubRunner) RunDir(_ string, _ string, _ ...string) ([]byte, error)     { return s.out, nil }

func TestHandler_PublicRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "public-response")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	h := New(router.New("github.com/private/*"), private.New(&stubRunner{}), public.New(u))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rsc.io/quote/@v/list", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "public-response", w.Body.String())
}

func TestHandler_PrivateRoute(t *testing.T) {
	runner := &stubRunner{out: []byte(`{"Versions":["v1.0.0"]}`)}
	h := New(
		router.New("github.com/private/*"),
		private.New(runner),
		public.New(mustURL(t, "http://127.0.0.1:1")), // should never be hit
	)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/github.com/private/repo/@v/list", nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "v1.0.0")
}

func TestHandler_BadPath(t *testing.T) {
	h := New(router.New(""), nil, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/not-a-module", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}
```

**Step 2: Run test, confirm failure**

```bash
go test ./internal/server/...
```

Expected: undefined `New`.

**Step 3: Implement**

`local-gomod-proxy/internal/server/server.go`:

```go
package server

import (
	"log/slog"
	"net/http"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
)

// New returns an http.Handler implementing the Go module proxy protocol.
// Routes private modules through the PrivateFetcher and public modules through
// the PublicFetcher.
func New(r *router.Router, priv *private.Fetcher, pub *public.Fetcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parsed, err := private.ParseRequest(req.URL.Path)
		if err != nil {
			slog.Info("bad request", "path", req.URL.Path, "err", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if r.IsPrivate(parsed.Module) {
			slog.Info("serving private", "module", parsed.Module, "version", parsed.Version)
			if err := priv.Serve(w, req, parsed); err != nil {
				slog.Error("private fetcher failed", "module", parsed.Module, "err", err)
				http.Error(w, "upstream error", http.StatusBadGateway)
			}
			return
		}

		slog.Info("serving public", "module", parsed.Module, "version", parsed.Version)
		pub.ServeHTTP(w, req)
	})
}
```

**Step 4: Run tests**

```bash
make test
```

Expected: all 3 server tests pass plus every earlier test still green.

**Step 5: Commit**

```bash
git add local-gomod-proxy/internal/server
git commit -m "feat(local-gomod-proxy): wire router and fetchers into HTTP handler"
```

---

## Task 11: `serve` subcommand

**Files:**

- Create: `local-gomod-proxy/cmd/local-gomod-proxy/serve.go`

Cobra subcommand. Reads `go env`, fails fast if `GOPRIVATE` is empty (unless `--private` overrides), warns if `GOVERSION < 1.21`, starts the HTTP server with the auth middleware.

Graceful shutdown: `signal.NotifyContext(os.Interrupt, syscall.SIGTERM)` → `srv.Shutdown(ctx)` on cancel.

**Step 1: Implement**

`local-gomod-proxy/cmd/local-gomod-proxy/serve.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/goenv"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/server"
	"github.com/spf13/cobra"
)

var (
	serveAddr     string
	servePrivate  string
	serveUpstream string
)

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":7070", "address to listen on")
	serveCmd.Flags().StringVar(&servePrivate, "private", "", "GOPRIVATE-style patterns (overrides `go env GOPRIVATE`)")
	serveCmd.Flags().StringVar(&serveUpstream, "upstream", "https://proxy.golang.org", "public upstream proxy URL")
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP proxy server",
	RunE: func(_ *cobra.Command, _ []string) error {
		runner := exec.NewOSRunner()
		env, err := goenv.Read(runner)
		if err != nil {
			return fmt.Errorf("reading go env: %w", err)
		}

		private_ := servePrivate
		if private_ == "" {
			private_ = env.GOPRIVATE
		}
		if private_ == "" {
			return errors.New("GOPRIVATE is not set. Run `go env -w GOPRIVATE=github.com/your-org/*` on the host, " +
				"or pass --private explicitly. With no private patterns, the proxy has no work to do.")
		}
		if env.GOMODCACHE == "" {
			return errors.New("GOMODCACHE is empty; ensure the host's go toolchain is configured")
		}
		if strings.HasPrefix(env.GOVERSION, "go1.1") || env.GOVERSION == "go1.20" {
			slog.Warn("host go version is older than 1.21; modules using the 'toolchain' directive may fail",
				"goversion", env.GOVERSION)
		}

		token, err := auth.EnsureToken(auth.TokenPath())
		if err != nil {
			return fmt.Errorf("ensuring auth token: %w", err)
		}

		upstream, err := url.Parse(serveUpstream)
		if err != nil {
			return fmt.Errorf("parsing upstream URL: %w", err)
		}

		handler := server.New(
			router.New(private_),
			private.New(runner),
			public.New(upstream),
		)
		handler = auth.Middleware(token, handler)

		srv := &http.Server{
			Addr:              serveAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}

		slog.Info("starting local-gomod-proxy",
			"addr", serveAddr,
			"goprivate", private_,
			"gomodcache", env.GOMODCACHE,
			"goversion", env.GOVERSION,
			"upstream", serveUpstream,
			"token_present", token != "")

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		errCh := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()

		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		case err := <-errCh:
			return err
		}
	},
}
```

**Step 2: Build**

```bash
cd local-gomod-proxy
go mod tidy
make build
```

Expected: `local-gomod-proxy` binary produced, no errors.

**Step 3: Smoke test `--help`**

```bash
./local-gomod-proxy serve --help
```

Expected: output shows `--addr`, `--private`, `--upstream` flags.

**Step 4: Commit**

```bash
git add local-gomod-proxy/cmd local-gomod-proxy/go.mod local-gomod-proxy/go.sum
git commit -m "feat(local-gomod-proxy): add serve subcommand"
```

---

## Task 12: `token` subcommand

**Files:**

- Create: `local-gomod-proxy/cmd/local-gomod-proxy/token.go`

Prints the current token to stdout for `sb` to capture.

**Step 1: Implement**

`local-gomod-proxy/cmd/local-gomod-proxy/token.go`:

```go
package main

import (
	"fmt"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(tokenCmd)
}

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print the current auth token (creates one if absent)",
	RunE: func(_ *cobra.Command, _ []string) error {
		tok, err := auth.EnsureToken(auth.TokenPath())
		if err != nil {
			return fmt.Errorf("ensuring auth token: %w", err)
		}
		fmt.Println(tok)
		return nil
	},
}
```

**Step 2: Build and smoke test**

```bash
make build
./local-gomod-proxy token
./local-gomod-proxy token  # second call returns the same value
```

Expected: 64-char hex string, identical on both calls.

**Step 3: Commit**

```bash
git add local-gomod-proxy/cmd
git commit -m "feat(local-gomod-proxy): add token subcommand"
```

---

## Task 13: integration test — PrivateFetcher against a local git repo

**Files:**

- Create: `local-gomod-proxy/internal/private/integration_test.go`

Uses `//go:build integration`. Creates a bare git repo in a temp dir, initializes it as a Go module with a tagged version, then invokes the real `PrivateFetcher` pointing at `file://` as if it were a private remote. Verifies the served `.zip` and `.info` actually contain the module source.

Pattern reference: look at `mcp-broker/test/e2e/` for how real subprocesses are handled.

**Step 1: Implement**

`local-gomod-proxy/internal/private/integration_test.go`:

```go
//go:build integration

package private

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	proxyExec "github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_ServesLocalGitModule(t *testing.T) {
	repo := setupGitRepo(t, "v0.1.0")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOPRIVATE", "example.com/*")
	// Rewrite the module path to our file:// remote.
	t.Setenv("GOFLAGS", "-mod=mod")
	home := t.TempDir()
	t.Setenv("HOME", home)
	// go env -w GONOSUMCHECK etc. lives in a file under HOME/.config/go/env.

	f := New(proxyExec.NewOSRunner())
	req := Request{
		Module:   "example.com/fake", // the module path declared in the repo's go.mod
		Version:  "v0.1.0",
		Artifact: ArtifactZip,
	}

	w := httptest.NewRecorder()
	require.NoError(t, f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req),
		"expected fetcher to succeed against %s", repo)

	// Assert: the zip contains a go.mod with the right module path.
	assert.Equal(t, http.StatusOK, w.Code)
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	require.NoError(t, err)
	found := false
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) == "go.mod" {
			rc, _ := zf.Open()
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			assert.Contains(t, string(b), "module example.com/fake")
			found = true
		}
	}
	assert.True(t, found, "expected go.mod in zip")
}

// setupGitRepo creates a bare git repo with a single-commit Go module tagged at the given version.
// Returns the file:// URL.
func setupGitRepo(t *testing.T, tag string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/fake\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package fake\n\nfunc Hello() string { return \"hi\" }\n"), 0o600))
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")
	run("git", "add", ".")
	run("git", "commit", "-m", "init")
	run("git", "tag", tag)
	return "file://" + dir
}
```

**Step 2: Run integration tests**

```bash
make test-integration
```

Expected: test passes (note: may be skipped if no git binary; acceptable as long as skipping is explicit).

If the test can't easily point `go mod download` at a `file://` repo (Go module resolution for `example.com/fake` will need `replace` or `GOINSECURE` tricks), simplify: **use a real public Go module as the test target under `ArtifactInfo`** and rely on network access — document the network requirement in the test file comment. Either variant is acceptable; TDD the behavior of the handler, not the infrastructure.

**Step 3: Commit**

```bash
git add local-gomod-proxy/internal/private
git commit -m "test(local-gomod-proxy): integration test for private fetcher"
```

---

## Task 14: E2E test — real binary against real `go mod download`

**Files:**

- Create: `local-gomod-proxy/test/e2e/e2e_test.go`

Uses `//go:build e2e`. Builds the binary, starts it on a random port, runs `GOPROXY=http://_:TOKEN@127.0.0.1:PORT/ go mod download rsc.io/quote@v1.5.2` in a scratch directory, asserts the module ends up in a local `GOMODCACHE`.

Pattern reference: `mcp-broker/test/e2e/` already does the "build real binary, spawn subprocess" dance.

**Step 1: Implement**

`local-gomod-proxy/test/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_PublicModuleRoundtrip(t *testing.T) {
	// Build the binary.
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "local-gomod-proxy")
	build := exec.Command("go", "build", "-o", bin, "./cmd/local-gomod-proxy")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build: %s", out)

	// Pick a free port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	// Fake a private pattern so the proxy starts up. Public fetch path still works.
	xdg := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyCmd := exec.CommandContext(ctx, bin, "serve", "--addr", addr, "--private", "example.invalid/private/*")
	proxyCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg)
	proxyCmd.Stdout = os.Stderr
	proxyCmd.Stderr = os.Stderr
	require.NoError(t, proxyCmd.Start())
	t.Cleanup(func() { cancel(); _ = proxyCmd.Wait() })

	// Grab the token.
	tokenCmd := exec.Command(bin, "token")
	tokenCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg)
	tokenOut, err := tokenCmd.Output()
	require.NoError(t, err)
	token := string(tokenOut[:64])

	// Wait for the proxy to be accepting connections.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy never started")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Download a real public module through our proxy.
	scratch := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o600))
	gomodcache := t.TempDir()
	download := exec.Command("go", "mod", "download", "-x", "rsc.io/quote@v1.5.2")
	download.Dir = scratch
	download.Env = append(os.Environ(),
		"GOPROXY=http://_:"+token+"@"+addr+"/",
		"GOSUMDB=off",
		"GOMODCACHE="+gomodcache,
	)
	out, err = download.CombinedOutput()
	require.NoError(t, err, "download: %s", out)

	// Verify the module ended up in GOMODCACHE.
	zipPath := filepath.Join(gomodcache, "cache/download/rsc.io/quote/@v/v1.5.2.zip")
	_, err = os.Stat(zipPath)
	assert.NoError(t, err, "expected %s to exist", zipPath)
}
```

**Step 2: Run E2E**

```bash
make test-e2e
```

Expected: test passes. Requires network access to `proxy.golang.org`.

**Step 3: Commit**

```bash
git add local-gomod-proxy/test
git commit -m "test(local-gomod-proxy): e2e test via real go mod download"
```

---

## Task 15: run `make audit`

**Step 1: Run full audit**

```bash
cd local-gomod-proxy
make audit
```

Expected: tidy, fmt, lint, test, govulncheck all pass. Fix anything that fails before committing.

**Step 2: If any changes were made (imports reordered, etc.), commit**

```bash
git add -u
git commit -m "chore(local-gomod-proxy): format, tidy, and lint"
```

---

## Task 16: project documentation — CLAUDE.md, DESIGN.md, README.md

**Files:**

- Create: `local-gomod-proxy/CLAUDE.md`
- Create: `local-gomod-proxy/DESIGN.md`
- Create: `local-gomod-proxy/README.md`

**Step 1: Write `local-gomod-proxy/CLAUDE.md`**

Model after `local-git-mcp/CLAUDE.md`. Include: development commands, architecture (matching final directory layout), and conventions specific to this tool (the `%w` / `%s` pattern, gosec nolint on `exec`, `--private` override, `GOPRIVATE` read via `go env`, token lives at XDG `local-gomod-proxy/auth-token`).

**Step 2: Write `local-gomod-proxy/DESIGN.md`**

Follow the structure of `local-git-mcp/DESIGN.md`: Motivation → Architecture → Protocol endpoints → Project structure → Validation/errors → Security → Tech stack → Design decisions → Testing. Use `.designs/2026-04-17-local-gomod-proxy.md` as source material — do not just copy it; the design doc should be presented from the perspective of "here's what this tool does" rather than "here's how we decided."

**Step 3: Write `local-gomod-proxy/README.md`**

User-facing. Should cover:

- What it is and why (~3 sentences).
- Install: `make install` from subdirectory or repo root.
- Run: `local-gomod-proxy serve [--addr :7070] [--private PATTERN]`.
- How the sandbox consumes it:
  ```sh
  export GOPROXY=http://_:$(local-gomod-proxy token)@host.lima.internal:7070/
  export GOSUMDB=off
  ```
- Security note: the token file lives at `$XDG_CONFIG_HOME/local-gomod-proxy/auth-token`.

**Step 4: Commit**

```bash
git add local-gomod-proxy/CLAUDE.md local-gomod-proxy/DESIGN.md local-gomod-proxy/README.md
git commit -m "docs(local-gomod-proxy): add CLAUDE.md, DESIGN.md, README.md"
```

---

## Task 17: update root docs

**Files:**

- Modify: `README.md` (repo root) — add `local-gomod-proxy` to the overview list and add a section describing it, parallel to `local-git-mcp` and `local-gh-mcp`.
- Modify: `CLAUDE.md` (repo root) — add `local-gomod-proxy/` to the Structure block.

**Step 1: Update root `README.md`**

In the Overview list (lines 7-13), insert the new tool in sorted order:

```markdown
- **[Local Gomod Proxy](#local-gomod-proxy)** — Host-side Go module proxy for sandboxed agents
```

In the install block (lines 30-37), add:

```bash
cd local-gomod-proxy && make install
```

Append a new `### Local Gomod Proxy` section after the `### Local GH MCP` section, following the problem/solution/bullets format of the surrounding entries. Use the design doc's Problem section as source material; keep to ~120 words.

**Step 2: Update root `CLAUDE.md`**

Add a line in the Structure block listing `local-gomod-proxy/` with a brief description.

**Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: add local-gomod-proxy to root README and CLAUDE.md"
```

---

## Done

At this point:

- `make install` from the repo root installs `local-gomod-proxy` alongside the other tools.
- `local-gomod-proxy serve` starts the proxy, ready for a Lima VM to point `GOPROXY` at it.
- All packages have unit tests; there's an integration test for the private fetcher and an e2e test exercising the full wire protocol.
- The root README and per-tool CLAUDE.md / DESIGN.md reflect the new tool.

No work remaining in this plan. Integration with `sandbox-manager` (provisioning the VM to set `GOPROXY` / `GOSUMDB`) is a separate effort.
