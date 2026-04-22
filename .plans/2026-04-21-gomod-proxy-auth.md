# local-gomod-proxy TLS + basic auth Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add TLS and HTTP basic auth to `local-gomod-proxy`, using a generated self-signed cert and random credentials persisted under `$XDG_STATE_HOME/local-gomod-proxy/`. The sandbox provisioning script reads both files via Lima's `$HOME` mount, installs the cert into the sandbox's system trust store (`sudo update-ca-certificates`), and sets `GOPROXY=https://x:<token>@host.lima.internal:7070/`.

**Note** (updates applied during execution):

1. The original design doc specified `GOINSECURE=host.lima.internal` in the sandbox. Task 7 established empirically that `GOINSECURE` does NOT trust an unknown cert for an HTTPS GOPROXY — it only enables HTTP fallback, which our HTTPS-only proxy can't satisfy. Tasks 8 and 9 reflect the corrected approach (trust-store install).
2. The original design assumed Lima's default `$HOME` mount would make host state visible inside the sandbox at the same path. `sandbox-manager` does NOT mount `$HOME` — it ships files in via the `copy_paths` config option. The provisioning script + Task 9's README now direct users to list both `cert.pem` and `credentials` under `copy_paths` so they land at the same `~/.local/state/local-gomod-proxy/` path inside the sandbox. Rotation still works: `sb provision` re-runs `copy_paths` before `scripts`.

**Architecture:** Two new internal packages — `internal/state/` (dir/cert/creds load-or-generate) and `internal/auth/` (basic-auth middleware). The `serve` command loads state on startup, wraps the existing handler with the auth middleware, and calls `ListenAndServeTLS` instead of `ListenAndServe`. No new flags except `--state-dir`. Plain-HTTP mode is removed — single deployment shape.

**Tech Stack:** Go stdlib (`crypto/ecdsa`, `crypto/rand`, `crypto/x509`, `crypto/tls`, `crypto/subtle`, `encoding/pem`, `net/http`), testify for assertions, existing `log/slog` for structured logs.

**Source design:** `.designs/2026-04-21-gomod-proxy-auth.md` is the spec — consult it when a task leaves a detail unspecified.

**Context for the implementer:**

- Module path: `github.com/averycrespi/agent-tools/local-gomod-proxy`. Go 1.25.9.
- All subprocess calls go through `internal/exec/Runner` — you won't add new exec calls here.
- Existing `internal/server/addr.go` enforces loopback-only binding. Keep it. TLS + auth layer on top; they do not replace it.
- Existing tests live next to the code (`foo_test.go`); integration uses `//go:build integration`; e2e uses `//go:build e2e` under `test/e2e/`.
- Commit messages: conventional commits, imperative mood, `<50` chars, no trailing period (e.g. `feat(state): add XDG state dir resolver`).
- Run `make audit` before the final commit of each task to catch `tidy/fmt/lint/test/govulncheck` regressions.

**Per-task commit flow:** after the test passes, run `make audit` from `local-gomod-proxy/`, then commit. If `audit` complains about formatting, `make fmt` first.

---

## Task 1: State directory resolver

**Files:**

- Create: `local-gomod-proxy/internal/state/dir.go`
- Create: `local-gomod-proxy/internal/state/dir_test.go`

**What it does:** Resolves the state directory from (in order) an explicit override, `$XDG_STATE_HOME/local-gomod-proxy`, or `$HOME/.local/state/local-gomod-proxy`. Ensures the directory exists with mode `0700`.

**Step 1: Write the failing test**

```go
// local-gomod-proxy/internal/state/dir_test.go
package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDir_OverrideWins(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom")
	got, err := ResolveDir(override)
	require.NoError(t, err)
	assert.Equal(t, override, got)
}

func TestResolveDir_XDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := ResolveDir("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "local-gomod-proxy"), got)
}

func TestResolveDir_HomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", t.TempDir())
	got, err := ResolveDir("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(os.Getenv("HOME"), ".local/state/local-gomod-proxy"), got)
}

func TestEnsureDir_Creates0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, EnsureDir(dir))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestEnsureDir_Idempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, EnsureDir(dir))
	require.NoError(t, EnsureDir(dir)) // second call must not fail
}
```

**Step 2: Run the test to verify it fails**

```bash
cd local-gomod-proxy && go test ./internal/state/...
```

Expected: compile error — `state` package does not exist.

**Step 3: Write minimal implementation**

```go
// local-gomod-proxy/internal/state/dir.go
// Package state loads or generates the TLS cert, private key, and basic-auth
// credentials that the proxy needs at startup. All files live under a single
// per-install state directory (default: $XDG_STATE_HOME/local-gomod-proxy).
package state

import (
	"fmt"
	"os"
	"path/filepath"
)

const dirName = "local-gomod-proxy"

// ResolveDir returns the absolute path to the proxy's state directory.
// Precedence: explicit override > $XDG_STATE_HOME/local-gomod-proxy >
// $HOME/.local/state/local-gomod-proxy.
func ResolveDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, dirName), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("cannot resolve state dir: XDG_STATE_HOME and HOME both unset")
	}
	return filepath.Join(home, ".local/state", dirName), nil
}

// EnsureDir creates dir with mode 0700 if it does not already exist.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir %s: %w", dir, err)
	}
	// MkdirAll does not chmod if the dir already existed with looser perms.
	// Tighten it defensively — these files are secrets.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod state dir %s: %w", dir, err)
	}
	return nil
}
```

**Step 4: Run the tests to verify they pass**

```bash
cd local-gomod-proxy && go test -race ./internal/state/...
```

Expected: PASS (5 tests).

**Step 5: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/internal/state/dir.go local-gomod-proxy/internal/state/dir_test.go
git commit -m "feat(state): add XDG state dir resolver"
```

---

## Task 2: Cert load-or-generate

**Files:**

- Create: `local-gomod-proxy/internal/state/cert.go`
- Create: `local-gomod-proxy/internal/state/cert_test.go`

**What it does:** `LoadOrGenerateCert(dir)` returns `(certPath, keyPath)`. It reuses existing `cert.pem`/`key.pem` if both parse and the cert has >30 days to expiry. Otherwise it generates a fresh ECDSA P-256 self-signed cert, writes `cert.pem` (0644) and `key.pem` (0600), and returns the paths. SANs: DNS `localhost`, `host.lima.internal`; IPs `127.0.0.1`, `::1`. Subject CN: `local-gomod-proxy`. Validity: 1 year.

**Step 1: Write the failing test**

```go
// local-gomod-proxy/internal/state/cert_test.go
package state

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerateCert_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "cert.pem"), certPath)
	assert.Equal(t, filepath.Join(dir, "key.pem"), keyPath)

	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm())

	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())

	cert := parseCertFile(t, certPath)
	assert.Equal(t, "local-gomod-proxy", cert.Subject.CommonName)
	assert.ElementsMatch(t, []string{"localhost", "host.lima.internal"}, cert.DNSNames)
	assert.Contains(t, cert.IPAddresses, net.ParseIP("127.0.0.1").To4())
	// Validity window: ~1 year.
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), cert.NotAfter, 24*time.Hour)
}

func TestLoadOrGenerateCert_ReusesWhenFresh(t *testing.T) {
	dir := t.TempDir()
	cert1, key1, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	certBytes1, _ := os.ReadFile(cert1)

	cert2, key2, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)
	certBytes2, _ := os.ReadFile(cert2)

	assert.Equal(t, cert1, cert2)
	assert.Equal(t, key1, key2)
	assert.Equal(t, certBytes1, certBytes2, "cert must not be regenerated when fresh")
}

func TestLoadOrGenerateCert_RegeneratesWhenNearExpiry(t *testing.T) {
	dir := t.TempDir()
	// Seed a cert that expires in 10 days — inside the 30-day renewal window.
	writeTestCert(t, dir, 10*24*time.Hour)
	certBytes1, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))

	_, _, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)

	certBytes2, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	assert.NotEqual(t, certBytes1, certBytes2, "cert should have been regenerated")

	cert := parseCertFile(t, filepath.Join(dir, "cert.pem"))
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), cert.NotAfter, 24*time.Hour)
}

func TestLoadOrGenerateCert_RegeneratesWhenUnparseable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("garbage"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), []byte("garbage"), 0o600))

	_, _, err := LoadOrGenerateCert(dir)
	require.NoError(t, err)

	cert := parseCertFile(t, filepath.Join(dir, "cert.pem"))
	assert.Equal(t, "local-gomod-proxy", cert.Subject.CommonName)
}

func parseCertFile(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(raw)
	require.NotNil(t, block, "expected PEM block")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// writeTestCert writes a self-signed cert/key pair with the given remaining
// validity, used to seed regen-path tests.
func writeTestCert(t *testing.T, dir string, validFor time.Duration) {
	t.Helper()
	_, _, err := generateCert(dir, validFor)
	require.NoError(t, err)
}
```

**Step 2: Run the test to verify it fails**

```bash
cd local-gomod-proxy && go test ./internal/state/...
```

Expected: compile error on `LoadOrGenerateCert` / `generateCert` undefined.

**Step 3: Write the implementation**

```go
// local-gomod-proxy/internal/state/cert.go
package state

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile      = "cert.pem"
	keyFile       = "key.pem"
	certValidity  = 365 * 24 * time.Hour
	renewalWindow = 30 * 24 * time.Hour
)

// LoadOrGenerateCert returns paths to the TLS cert and key in dir. If both
// files exist, parse cleanly, and the cert has more than renewalWindow left
// before expiry, the existing pair is reused. Otherwise a fresh ECDSA P-256
// self-signed cert is generated and written in-place.
func LoadOrGenerateCert(dir string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	if reusable(certPath, keyPath) {
		return certPath, keyPath, nil
	}
	slog.Info("generating new TLS cert", "dir", dir, "validity", certValidity)
	return generateCert(dir, certValidity)
}

func reusable(certPath, keyPath string) bool {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return time.Until(cert.NotAfter) > renewalWindow
}

func generateCert(dir string, validFor time.Duration) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating ECDSA key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generating serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "local-gomod-proxy"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost", "host.lima.internal"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("creating certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("writing cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshaling key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("writing key: %w", err)
	}

	return certPath, keyPath, nil
}
```

**Step 4: Run the tests to verify they pass**

```bash
cd local-gomod-proxy && go test -race ./internal/state/...
```

Expected: all 9 tests pass.

**Step 5: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/internal/state/cert.go local-gomod-proxy/internal/state/cert_test.go
git commit -m "feat(state): add TLS cert load-or-generate"
```

---

## Task 3: Credentials load-or-generate

**Files:**

- Create: `local-gomod-proxy/internal/state/creds.go`
- Create: `local-gomod-proxy/internal/state/creds_test.go`

**What it does:** `LoadOrGenerateCredentials(dir)` returns a `Credentials{Username, Password}`. The credentials file format is exactly one line: `x:<token>\n` at mode `0600`. Username is always `x`. Password is 32 random bytes base64url-encoded (no padding). Missing file → generate. Present + well-formed → reuse. Present + malformed → error (do **not** clobber; the user may have hand-edited).

**Step 1: Write the failing test**

```go
// local-gomod-proxy/internal/state/creds_test.go
package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerateCredentials_GeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	assert.Equal(t, "x", creds.Username)
	assert.NotEmpty(t, creds.Password)
	// base64url of 32 bytes is 43 chars (no padding).
	assert.Len(t, creds.Password, 43)

	path := filepath.Join(dir, "credentials")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "x:"+creds.Password+"\n", string(raw))
}

func TestLoadOrGenerateCredentials_ReusesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	c1, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	c2, err := LoadOrGenerateCredentials(dir)
	require.NoError(t, err)
	assert.Equal(t, c1, c2)
}

func TestLoadOrGenerateCredentials_MalformedFailsHard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	require.NoError(t, os.WriteFile(path, []byte("not-a-valid-line"), 0o600))

	_, err := LoadOrGenerateCredentials(dir)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "malformed")

	// File must not have been clobbered.
	raw, _ := os.ReadFile(path)
	assert.Equal(t, "not-a-valid-line", string(raw))
}

func TestLoadOrGenerateCredentials_WrongUsernameFailsHard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	require.NoError(t, os.WriteFile(path, []byte("admin:hunter2\n"), 0o600))

	_, err := LoadOrGenerateCredentials(dir)
	require.Error(t, err)
}

func TestLoadOrGenerateCredentials_GeneratesDifferentPasswords(t *testing.T) {
	c1, err := LoadOrGenerateCredentials(t.TempDir())
	require.NoError(t, err)
	c2, err := LoadOrGenerateCredentials(t.TempDir())
	require.NoError(t, err)
	assert.NotEqual(t, c1.Password, c2.Password)
}
```

**Step 2: Run the test to verify it fails**

```bash
cd local-gomod-proxy && go test ./internal/state/...
```

Expected: compile error on undefined `LoadOrGenerateCredentials`.

**Step 3: Write the implementation**

```go
// local-gomod-proxy/internal/state/creds.go
package state

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	credsFile       = "credentials"
	credsUsername   = "x"
	credsTokenBytes = 32
)

// Credentials holds the basic-auth username/password pair that the proxy
// enforces on every incoming request.
type Credentials struct {
	Username string
	Password string
}

// LoadOrGenerateCredentials reads the credentials file at dir/credentials, or
// generates a fresh "x:<random>" pair and writes it if the file is missing.
// A present-but-malformed file is a hard error: the user may have hand-edited
// it, and silently regenerating would invalidate every provisioned sandbox.
func LoadOrGenerateCredentials(dir string) (Credentials, error) {
	path := filepath.Join(dir, credsFile)

	raw, err := os.ReadFile(path)
	if err == nil {
		return parseCredentials(raw)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Credentials{}, fmt.Errorf("reading credentials: %w", err)
	}

	creds, err := generateCredentials()
	if err != nil {
		return Credentials{}, err
	}
	line := creds.Username + ":" + creds.Password + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return Credentials{}, fmt.Errorf("writing credentials: %w", err)
	}
	return creds, nil
}

func parseCredentials(raw []byte) (Credentials, error) {
	line := strings.TrimRight(string(raw), "\n")
	user, pass, ok := strings.Cut(line, ":")
	if !ok || user == "" || pass == "" || strings.ContainsAny(line, "\n\r") {
		return Credentials{}, fmt.Errorf("credentials file is malformed; delete it to regenerate")
	}
	if user != credsUsername {
		return Credentials{}, fmt.Errorf("credentials file is malformed: username must be %q", credsUsername)
	}
	return Credentials{Username: user, Password: pass}, nil
}

func generateCredentials() (Credentials, error) {
	buf := make([]byte, credsTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return Credentials{}, fmt.Errorf("reading random: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return Credentials{Username: credsUsername, Password: token}, nil
}
```

**Step 4: Run the tests to verify they pass**

```bash
cd local-gomod-proxy && go test -race ./internal/state/...
```

Expected: all state tests pass.

**Step 5: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/internal/state/creds.go local-gomod-proxy/internal/state/creds_test.go
git commit -m "feat(state): add credentials load-or-generate"
```

---

## Task 4: Basic-auth middleware

**Files:**

- Create: `local-gomod-proxy/internal/auth/auth.go`
- Create: `local-gomod-proxy/internal/auth/auth_test.go`

**What it does:** `Middleware(next, creds)` returns an `http.Handler` that enforces HTTP Basic auth. Matches both username and password with `subtle.ConstantTimeCompare` on byte slices of equal length. On mismatch / missing header: responds with `401` and `WWW-Authenticate: Basic realm="local-gomod-proxy"`; logs at Warn with remote address only (never the Authorization header). The realm name is a constant; the error body is a plain string (no enumeration aid).

**Step 1: Write the failing test**

```go
// local-gomod-proxy/internal/auth/auth_test.go
package auth

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
})

var testCreds = state.Credentials{Username: "x", Password: "s3cret-token"}

func TestMiddleware_ValidCredsPassThrough(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", "s3cret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestMiddleware_WrongPassword_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", "wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="local-gomod-proxy"`, w.Header().Get("WWW-Authenticate"))
}

func TestMiddleware_WrongUsername_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "s3cret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_MissingHeader_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Basic realm="local-gomod-proxy"`, w.Header().Get("WWW-Authenticate"))
}

func TestMiddleware_MalformedHeader_401(t *testing.T) {
	h := Middleware(ok, testCreds)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer totally-not-basic")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_NeverLogsAuthorizationValue(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := Middleware(ok, testCreds)

	secret := "this-exact-secret-must-not-appear-in-logs"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)

	assert.NotContains(t, logBuf.String(), secret)
	assert.NotContains(t, logBuf.String(), "Authorization")
}
```

**Step 2: Run the test to verify it fails**

```bash
cd local-gomod-proxy && go test ./internal/auth/...
```

Expected: compile error — `auth` package does not exist.

**Step 3: Write the implementation**

```go
// local-gomod-proxy/internal/auth/auth.go
// Package auth enforces HTTP Basic authentication on every request reaching
// the proxy. Credentials come from internal/state; see DESIGN.md for the
// trust model.
package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
)

const realm = "local-gomod-proxy"

// Middleware wraps next with HTTP Basic auth enforcement. Requests missing or
// carrying bad credentials are rejected with 401 before reaching next.
func Middleware(next http.Handler, creds state.Credentials) http.Handler {
	wantUser := []byte(creds.Username)
	wantPass := []byte(creds.Password)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || !secureEqual([]byte(user), wantUser) || !secureEqual([]byte(pass), wantPass) {
			// Log only the remote address — never the Authorization header or
			// any user-supplied bytes, which could include the password.
			slog.Warn("auth failed", "remote", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// secureEqual compares two byte slices in constant time relative to their
// length. It returns false for length-mismatched inputs (ConstantTimeCompare
// returns 0 in that case, which we report as a mismatch).
func secureEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
```

**Step 4: Run the tests to verify they pass**

```bash
cd local-gomod-proxy && go test -race ./internal/auth/...
```

Expected: all 6 tests pass.

**Step 5: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/internal/auth/auth.go local-gomod-proxy/internal/auth/auth_test.go
git commit -m "feat(auth): add basic-auth middleware"
```

---

## Task 5: Wire state + auth + TLS into `serve`

**Files:**

- Modify: `local-gomod-proxy/cmd/local-gomod-proxy/serve.go`

**What it does:** On startup, resolve + ensure the state dir, load-or-generate cert and creds, wrap the server handler with `auth.Middleware`, and replace `srv.ListenAndServe()` with `srv.ListenAndServeTLS(certPath, keyPath)`. Log the resolved state dir, cert SHA-256 fingerprint (first 16 hex chars), and listen addr at Info. Never log the password. Add a `--state-dir` string flag with empty default (falls through to `ResolveDir("")`).

**Read first:** `local-gomod-proxy/cmd/local-gomod-proxy/serve.go` — you'll replace the flag block, the post-env setup, the `srv` config, the startup log, and the `ListenAndServe` call.

**Step 1: Update the file**

Add a new flag variable alongside the existing ones:

```go
var (
	serveAddr     string
	servePrivate  string
	serveUpstream string
	serveStateDir string
)
```

Register the flag inside `init()`:

```go
serveCmd.Flags().StringVar(&serveStateDir, "state-dir", "",
	"directory for TLS cert + credentials (default $XDG_STATE_HOME/local-gomod-proxy)")
```

Inside the `RunE` closure, after the existing `upstream` parse block but **before** `handler := server.New(...)`, insert:

```go
stateDir, err := state.ResolveDir(serveStateDir)
if err != nil {
	return fmt.Errorf("resolving state dir: %w", err)
}
if err := state.EnsureDir(stateDir); err != nil {
	return err
}
certPath, keyPath, err := state.LoadOrGenerateCert(stateDir)
if err != nil {
	return fmt.Errorf("loading cert: %w", err)
}
creds, err := state.LoadOrGenerateCredentials(stateDir)
if err != nil {
	return fmt.Errorf("loading credentials: %w", err)
}
fingerprint, err := certFingerprint(certPath)
if err != nil {
	return fmt.Errorf("reading cert fingerprint: %w", err)
}
```

Wrap the handler:

```go
handler := auth.Middleware(
	server.New(
		router.New(private_),
		private.New(runner),
		public.New(upstream),
		maxConcurrentPrivate,
	),
	creds,
)
```

Update the `slog.Info("starting local-gomod-proxy", ...)` call to include `"state_dir", stateDir` and `"cert_fp", fingerprint`. Do **not** add password or anything derived from it.

Set a minimum TLS version on the server (insert before the goroutine):

```go
srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
```

Replace the listener call inside the goroutine:

```go
if err := srv.ListenAndServeTLS(certPath, keyPath); err != nil && !errors.Is(err, http.ErrServerClosed) {
	errCh <- err
}
```

Add the fingerprint helper at the bottom of the file:

```go
// certFingerprint returns the first 16 hex chars of the SHA-256 over the
// leaf cert DER. Logged at startup so operators can confirm which cert was
// loaded without touching the key material.
func certFingerprint(certPath string) (string, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", certPath)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])[:16], nil
}
```

Add imports: `"crypto/sha256"`, `"crypto/tls"`, `"encoding/hex"`, `"encoding/pem"`, plus the two new internal packages `internal/auth` and `internal/state`. `"os"` is already imported.

**Step 2: Build to verify it compiles**

```bash
cd local-gomod-proxy && go build ./...
```

Expected: no errors.

**Step 3: Smoke-test the happy path manually**

```bash
cd local-gomod-proxy
rm -rf /tmp/gomod-proxy-state
go run ./cmd/local-gomod-proxy serve --addr 127.0.0.1:17070 --private 'github.com/never-match/*' --state-dir /tmp/gomod-proxy-state &
SERVE_PID=$!
sleep 1

# Extract the token.
creds=$(cat /tmp/gomod-proxy-state/credentials)

# Auth-less request should 401.
curl -ks -o /dev/null -w '%{http_code}\n' https://127.0.0.1:17070/rsc.io/quote/@v/list
# Expected: 401

# With creds should 200 (public path reverse-proxies to proxy.golang.org).
curl -ks -o /dev/null -w '%{http_code}\n' -u "$creds" https://127.0.0.1:17070/rsc.io/quote/@v/list
# Expected: 200

kill $SERVE_PID
wait $SERVE_PID 2>/dev/null
```

Expected outputs marked inline. Files written: `/tmp/gomod-proxy-state/{cert.pem,key.pem,credentials}` with modes 0644/0600/0600.

**Step 4: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/cmd/local-gomod-proxy/serve.go
git commit -m "feat(serve): switch listener to TLS + basic auth"
```

---

## Task 6: Server integration test — TLS + auth roundtrip

**Files:**

- Create: `local-gomod-proxy/internal/server/tls_integration_test.go`

**What it does:** Starts an actual `httptest.NewTLSServer` wrapping the authenticated handler, hits it with a TLS-aware client, and asserts the 200/401 shape. Tagged `integration` because it exercises real crypto + net stack, but it does **not** need network access (upstream is a stub).

**Read first:** `local-gomod-proxy/internal/server/server_test.go` to see the existing `stubRunner` pattern.

**Step 1: Write the test**

```go
//go:build integration

// local-gomod-proxy/internal/server/tls_integration_test.go
package server

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/auth"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/private"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/public"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/router"
	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_TLSAndAuth spins up the full handler behind TLS and asserts
// that auth is required and honored.
func TestIntegration_TLSAndAuth(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := state.LoadOrGenerateCert(dir)
	require.NoError(t, err)
	creds, err := state.LoadOrGenerateCredentials(dir)
	require.NoError(t, err)

	// Upstream stub: if the request reaches it, we returned 200.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

	handler := auth.Middleware(
		New(
			router.New("github.com/never-match/*"),
			private.New(&stubRunner{}),
			public.New(u),
			8,
		),
		creds,
	)

	// Start a TLS server using the generated cert.
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	certPEM, err := os.ReadFile(certPath)
	require.NoError(t, err)
	require.True(t, pool.AppendCertsFromPEM(certPEM))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	t.Run("no creds → 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, `Basic realm="local-gomod-proxy"`, resp.Header.Get("WWW-Authenticate"))
	})

	t.Run("wrong creds → 401", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		req.SetBasicAuth("x", "wrong")
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct creds → 200", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rsc.io/quote/@v/list", nil)
		req.SetBasicAuth(creds.Username, creds.Password)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "upstream-ok", string(body))
	})
}
```

**Step 2: Run the test to verify it passes**

```bash
cd local-gomod-proxy && go test -race -tags=integration ./internal/server/...
```

Expected: PASS (the existing server tests still run under this tag too).

**Step 3: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/internal/server/tls_integration_test.go
git commit -m "test(server): add TLS + auth integration test"
```

---

## Task 7: Update E2E test for TLS + auth

**Files:**

- Modify: `local-gomod-proxy/test/e2e/e2e_test.go`

**What it does:** The E2E test builds and runs the real binary. Since plain HTTP is gone, the test must now (a) pass a `--state-dir`, (b) read the generated credentials, and (c) set `GOPROXY=https://x:<token>@<addr>/` + `GOINSECURE=<host>` on the client `go mod download` subprocess.

**Read first:** `local-gomod-proxy/test/e2e/e2e_test.go` — understand `startProxy` and `downloadThroughProxy`.

**Step 1: Update `startProxy` to take and return a state dir**

Change the signature to return `(addr, creds string)`:

```go
func startProxy(t *testing.T, bin, private string, extraEnv []string) (addr, creds string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = l.Addr().String()
	require.NoError(t, l.Close())

	stateDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, bin, "serve",
		"--addr", addr,
		"--private", private,
		"--state-dir", stateDir,
	)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })

	// Wait for the listener AND the credentials file to both be ready.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			if raw, err := os.ReadFile(filepath.Join(stateDir, "credentials")); err == nil {
				creds = strings.TrimRight(string(raw), "\n")
				return addr, creds
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
```

Add `"strings"` to the imports.

**Step 2: Update `downloadThroughProxy` to take creds + use HTTPS + GOINSECURE**

```go
func downloadThroughProxy(t *testing.T, proxyAddr, creds, modVersion string, extraEnv []string) string {
	t.Helper()
	scratch := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o600))

	cache := newGOMODCACHE(t)

	host, _, err := net.SplitHostPort(proxyAddr)
	require.NoError(t, err)

	env := append([]string{
		fmt.Sprintf("GOPROXY=https://%s@%s/", creds, proxyAddr),
		"GOINSECURE=" + host,
		"GOSUMDB=off",
		"GOPRIVATE=",
		"GONOPROXY=",
		"GOMODCACHE=" + cache,
	}, extraEnv...)

	cmd := exec.Command("go", "mod", "download", "-x", modVersion)
	cmd.Dir = scratch
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "download %s: %s", modVersion, out)
	return cache
}
```

**Step 3: Update both test functions to thread the new creds return**

Replace each `addr := startProxy(t, ...)` with `addr, creds := startProxy(t, ...)` and each `downloadThroughProxy(t, addr, ...)` with `downloadThroughProxy(t, addr, creds, ...)`.

**Step 4: Run the E2E tests to verify they pass**

```bash
cd local-gomod-proxy && make test-e2e
```

Expected: both `TestE2E_PublicModuleRoundtrip` and `TestE2E_PrivateModuleRoundtrip` PASS.

If they fail with a `tls: failed to verify` error, double-check that `GOINSECURE=<host>` matches the host portion of the addr exactly (should be `127.0.0.1`).

**Step 5: Audit and commit**

```bash
cd local-gomod-proxy && make audit
git add local-gomod-proxy/test/e2e/e2e_test.go
git commit -m "test(e2e): thread TLS + auth through E2E harness"
```

---

## Task 8: Update sandbox provisioning script

**Files:**

- Modify: `local-gomod-proxy/examples/provision/gomod-proxy.sh`

**What it does:** The script runs inside the Lima sandbox as the sandbox user. It (a) reads the host-written cert and credentials via Lima's `$HOME` mount, (b) installs the cert into the sandbox's system trust store via `sudo update-ca-certificates`, and (c) writes a marker-fenced `GOPROXY` block to `~/.bashrc`. Both steps are idempotent.

**Design rationale** (reflect in DESIGN.md in Task 9): Task 7 established empirically that `GOINSECURE` does NOT make Go trust an unknown cert for an HTTPS GOPROXY — it only enables HTTP fallback. Since our proxy is HTTPS-only, we need a real trust mechanism. Two viable paths:

1. **System trust store via `update-ca-certificates`** — works for every tool (go, curl, python), but couples sandbox state to host cert rotation (re-run this script after host regenerates the cert, which happens annually).
2. **`SSL_CERT_FILE` globally** — simpler for rotation but REPLACES system roots for every tool that honors the env var, breaking HTTPS to anything not signed by our cert.

We go with (1). Lima sandboxes have passwordless sudo, so the install step runs non-interactively. Cert rotation is annual at most.

**Read first:** current `local-gomod-proxy/examples/provision/gomod-proxy.sh` — note the marker-fenced idempotency pattern and the existing `GOPRIVATE` clearing logic.

**Step 1: Replace the script body**

```sh
#!/bin/bash
# Configure a Lima sandbox to route Go module resolution through the host's
# local-gomod-proxy over HTTPS + basic auth.
#
# Depends on the host running local-gomod-proxy (either manually via
# `local-gomod-proxy serve` or via the launchd agent; see docs/launchd.md).
# That process writes the TLS cert to
# $HOME/.local/state/local-gomod-proxy/cert.pem and credentials to
# $HOME/.local/state/local-gomod-proxy/credentials. Lima's default $HOME
# mount makes both visible inside the sandbox at the same path.
#
# This script:
#   (a) installs the proxy's self-signed cert into the sandbox's system
#       trust store via sudo update-ca-certificates (Lima sandboxes have
#       passwordless sudo).
#   (b) writes a marker-fenced GOPROXY block to ~/.bashrc.
# Both steps are idempotent.
#
# Cert rotation: if the host regenerates the cert (annual at expiry, or
# manual via rm -rf $state_dir), re-run this script in the sandbox to
# refresh the trust store.

set -euo pipefail

command_exists() { command -v "$1" &>/dev/null; }

if ! command_exists go; then
	echo "error: go not found on PATH — install Go first (e.g. asdf-golang.sh)" >&2
	exit 1
fi
if ! command_exists update-ca-certificates; then
	echo "error: update-ca-certificates not found; this script targets Debian/Ubuntu sandboxes" >&2
	exit 1
fi

STATE_DIR="$HOME/.local/state/local-gomod-proxy"
CERT_FILE="$STATE_DIR/cert.pem"
CREDS_FILE="$STATE_DIR/credentials"
INSTALLED_CERT="/usr/local/share/ca-certificates/local-gomod-proxy.crt"

if [[ ! -r "$CERT_FILE" || ! -r "$CREDS_FILE" ]]; then
	cat >&2 <<EOF
error: missing $CERT_FILE and/or $CREDS_FILE.

Start the proxy on the host first — it creates both files on first launch:
  local-gomod-proxy serve
or install the launchd agent (see local-gomod-proxy/docs/launchd.md).

If the host is running but the files still aren't visible, confirm that Lima's
default \$HOME mount is enabled for this VM (see \`limactl list --json\`).
EOF
	exit 1
fi

# File format is a single line "x:<token>\n". Strip the trailing newline.
CREDS="$(tr -d '\n' < "$CREDS_FILE")"
if [[ -z "$CREDS" || "$CREDS" != x:* ]]; then
	echo "error: $CREDS_FILE is malformed (expected 'x:<token>')" >&2
	exit 1
fi

# Install cert into the system trust store. update-ca-certificates picks up
# anything in /usr/local/share/ca-certificates/*.crt. Skip the rewrite +
# trust-store rebuild if the installed cert already matches byte-for-byte.
if ! [[ -f "$INSTALLED_CERT" ]] || ! sudo cmp -s "$CERT_FILE" "$INSTALLED_CERT"; then
	echo "Installing local-gomod-proxy cert into system trust store"
	sudo cp "$CERT_FILE" "$INSTALLED_CERT"
	sudo update-ca-certificates >/dev/null
else
	echo "local-gomod-proxy cert already in system trust store, skipping"
fi

MARKER_START="# >>> local-gomod-proxy >>>"
MARKER_END="# <<< local-gomod-proxy <<<"

if grep -qF "$MARKER_START" "$HOME/.bashrc" 2>/dev/null; then
	echo "local-gomod-proxy env already configured in ~/.bashrc, skipping"
	exit 0
fi

echo "Configuring GOPROXY in ~/.bashrc"
cat >>"$HOME/.bashrc" <<EOF

$MARKER_START
# Route Go module resolution through the host's local-gomod-proxy over HTTPS.
# The proxy's self-signed cert is installed into the sandbox's system trust
# store (see the install step in gomod-proxy.sh) so Go can verify it.
# Credentials live in \$HOME/.local/state/local-gomod-proxy/credentials on the
# host and are visible at the same path inside the sandbox via Lima's \$HOME
# mount.
export GOPROXY="https://\$(tr -d '\n' < \$HOME/.local/state/local-gomod-proxy/credentials)@host.lima.internal:7070/"
# go.sum (committed to the repo) is the primary integrity check; disable the
# public checksum database so private modules don't leak to sum.golang.org.
export GOSUMDB=off
# Defense in depth: even if something re-sets GOPRIVATE via the environment,
# matching modules should still route through GOPROXY.
unset GOPRIVATE
$MARKER_END
EOF
```

**Step 2: Lint the script**

```bash
bash -n local-gomod-proxy/examples/provision/gomod-proxy.sh
shellcheck local-gomod-proxy/examples/provision/gomod-proxy.sh || true
```

Expected: `bash -n` exits 0. Address any shellcheck findings that aren't already present in the original. Full dry-run is not practical (requires a sandbox with passwordless sudo and the `update-ca-certificates` binary); syntax check is enough.

**Step 3: Commit**

```bash
git add local-gomod-proxy/examples/provision/gomod-proxy.sh
git commit -m "feat(provision): install proxy cert into sandbox trust store"
```

---

## Task 9: Update documentation

**Files:**

- Modify: `local-gomod-proxy/DESIGN.md`
- Modify: `local-gomod-proxy/README.md`
- Modify: `local-gomod-proxy/CLAUDE.md`
- Modify: `local-gomod-proxy/docs/launchd.md`

**Scope:** Per the doc-sync rule in `local-gomod-proxy/CLAUDE.md`, any flag/endpoint/env/layout change is audited across all four. The current texts claim the proxy is "unauthenticated" and uses "plain HTTP" — both need to go.

### DESIGN.md

1. In the **Motivation** section, rewrite the paragraph that currently describes the proxy as "unauthenticated" to state that the proxy now uses TLS + basic auth layered on top of loopback binding.
2. Update the ASCII diagram around line 17 so the arrow between sandbox and host reads `──HTTPS (basic auth)──►` instead of `──HTTP──►`, and so the GOPROXY line shows `https://x:…@host.lima.internal:7070/`.
3. Add a new subsection **State directory** under Architecture (after "Request flow") describing the layout:

   ```
   $XDG_STATE_HOME/local-gomod-proxy/
   ├── cert.pem       (0644) — self-signed ECDSA P-256, SANs localhost/127.0.0.1/host.lima.internal, 1y
   ├── key.pem        (0600)
   └── credentials    (0600) — single line "x:<token>\n"
   ```

4. In **Security**, replace the "Local-only deployment — the proxy is unauthenticated" bullet with:
   - **Authenticated over TLS** — every request requires HTTP Basic auth against a credentials file in `$XDG_STATE_HOME/local-gomod-proxy/credentials`. TLS uses a generated self-signed cert. The listener is still loopback-only; TLS + auth add defense-in-depth against same-user processes on the host, not replace the network boundary.
   - Replace the "Plain HTTP" bullet with: **TLS, self-signed cert** — traffic stays on the Lima bridge; cert rotation is manual via `rm -rf $state_dir` followed by restart + sandbox re-provision.
5. In **Design decisions**, replace the "No application-level auth" and "Plain HTTP, no TLS" entries with:
   - **HTTPS + basic auth, not plain HTTP.** Loopback-only binding protects against other hosts; it does not protect against other processes running as the same user (browser extensions, random CLIs that probe `localhost:7070`). Requiring an auth token from a `0600` file they have no reason to read adds cheap defense-in-depth. TLS is required because every auth mechanism the `go` tool supports (URL-embedded, `.netrc`, `GOAUTH`) is HTTPS-gated since Go 1.22.
   - **Self-signed cert installed into the sandbox trust store** (not `GOINSECURE`). `GOINSECURE` does not make Go trust an unknown cert for an HTTPS GOPROXY — it only enables HTTP fallback on cert-verification failure, which our HTTPS-only proxy can't satisfy. The alternatives are (a) install the cert into the sandbox's system CA store via `update-ca-certificates`, or (b) export `SSL_CERT_FILE` pointing at the cert. (b) is rejected because setting `SSL_CERT_FILE` globally REPLACES the system root set for every tool that honors the env var (curl, python, etc.), breaking HTTPS to anything else. (a) costs one `sudo update-ca-certificates` at provision time (Lima sandboxes have passwordless sudo) and one re-provision per cert rotation (annual at most).
6. In **Testing**, add the new `internal/state` and `internal/auth` unit suites and the `tls_integration_test.go` to the Unit and Integration rows.

### README.md

1. Rewrite the banner blockquote: replace "runs unauthenticated over plain HTTP" with a new, tighter warning that (a) explicit loopback binding is still required, (b) the credentials file at `$XDG_STATE_HOME/local-gomod-proxy/credentials` is a host-local secret, and (c) overriding `--addr` to non-loopback is still unsupported.
2. In **Run** → flag table, add:

   | `--state-dir` | `$XDG_STATE_HOME/local-gomod-proxy` | Directory for TLS cert + credentials. Defaults under `~/.local/state/` if `$XDG_STATE_HOME` is unset. |

3. Rewrite **How the sandbox consumes it** to reflect the `copy_paths` + `scripts` pairing. The sandbox does NOT mount the host's `$HOME` — `sandbox-manager` ships files in via `copy_paths`. Show a config snippet:

   ```json
   {
     "copy_paths": [
       "~/.local/state/local-gomod-proxy/cert.pem",
       "~/.local/state/local-gomod-proxy/credentials"
     ],
     "scripts": [
       "/path/to/agent-tools/local-gomod-proxy/examples/provision/gomod-proxy.sh"
     ]
   }
   ```

   Both files land at the same `~/.local/state/local-gomod-proxy/` path inside the sandbox. The provisioning script then installs the cert into the system trust store (`sudo update-ca-certificates`) and writes `GOPROXY` to `~/.bashrc`. Cert rotation: re-run `sb provision` after the host regenerates its cert — `copy_paths` re-runs before `scripts`, so the new cert flows through transparently.

   For sandboxes not using `sandbox-manager`, document the equivalent manual steps: copy both files in via whatever mechanism the caller uses, install the cert (`sudo cp ... /usr/local/share/ca-certificates/local-gomod-proxy.crt && sudo update-ca-certificates`), then export `GOPROXY=https://$(tr -d '\n' < ~/.local/state/local-gomod-proxy/credentials)@host.lima.internal:7070/`, `GOSUMDB=off`, `unset GOPRIVATE`.

4. Replace the **Security** section with three bullets that map to the "Honest limits" list in `.designs/2026-04-21-gomod-proxy-auth.md`:
   - What's blocked: browser JS, casual `localhost` probes, any process that doesn't know to read the credentials file.
   - What isn't: any process running as the same user can `cat` the credentials and use them — `0600` stops other users, not other processes of yours.
   - Rotation: `rm -rf $XDG_STATE_HOME/local-gomod-proxy && restart proxy && re-run provisioning in every sandbox`.

### CLAUDE.md

In the **Conventions** list, replace the current bullet:

```
- No application-level auth — the proxy relies on binding to a local-only interface. See DESIGN.md for rationale ...
```

with:

```
- Auth: HTTPS + HTTP basic auth enforced on every request. Cert, key, and credentials live under `$XDG_STATE_HOME/local-gomod-proxy/` (fallback `~/.local/state/local-gomod-proxy/`). Loopback binding is still enforced — TLS + auth layer on top, they do not replace the network boundary.
```

Also add a new entry to the internal-packages list in **Architecture**:

```
  state/                 State dir, cert load-or-generate, credentials load-or-generate
  auth/                  HTTP Basic auth middleware
```

### docs/launchd.md

1. Add a new short section after the first `## Authenticate git without an ssh-agent` section, titled `## State directory`, that notes: (a) the state dir is created on first launch, same user as the launchd job — no extra plist wiring needed; (b) `$XDG_STATE_HOME` is usually unset under launchd, so the fallback path `~/.local/state/local-gomod-proxy/` is what you'll actually see.
2. Update the `curl` smoke test in **Verify** to use HTTPS + the credentials file:

   ```bash
   creds=$(cat ~/.local/state/local-gomod-proxy/credentials)
   curl -ksI -u "$creds" https://127.0.0.1:7070/github.com/stretchr/testify/@latest
   ```

**Step 1: Edit all four files per the spec above**

Make each change inline using `Edit`/`Write`.

**Step 2: Re-read each file and spot-check**

```bash
cd local-gomod-proxy
grep -n -i 'unauthenticated\|plain http\|plain-http' DESIGN.md README.md CLAUDE.md docs/launchd.md || echo "clean"
```

Expected: `clean` (the phrase should only survive inside historical/alternatives-considered context, if at all).

**Step 3: Commit**

```bash
git add local-gomod-proxy/DESIGN.md local-gomod-proxy/README.md local-gomod-proxy/CLAUDE.md local-gomod-proxy/docs/launchd.md
git commit -m "docs(local-gomod-proxy): describe TLS + basic auth"
```

---

## Final verification

After all tasks complete:

```bash
cd local-gomod-proxy
make audit && make test-integration && make test-e2e
```

Expected: all green. Then `git log --oneline main..HEAD` should show one commit per task, in order.
