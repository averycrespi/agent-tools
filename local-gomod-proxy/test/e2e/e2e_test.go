//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_PublicModuleRoundtrip exercises the public-module path end-to-end:
// the proxy reverse-proxies to proxy.golang.org. The GOPRIVATE pattern
// (example.invalid) is deliberately non-matching so every request routes
// through PublicFetcher.
func TestE2E_PublicModuleRoundtrip(t *testing.T) {
	bin := buildBinary(t)
	addr, creds, certPath := startProxy(t, bin, "example.invalid/private/*", nil)

	clientCache := downloadThroughProxy(t, addr, creds, certPath, "rsc.io/quote@v1.5.2", nil)

	zipPath := filepath.Join(clientCache, "cache/download/rsc.io/quote/@v/v1.5.2.zip")
	_, err := os.Stat(zipPath)
	assert.NoError(t, err, "expected %s to exist", zipPath)
}

// TestE2E_PrivateModuleRoundtrip exercises the private-module path end-to-end:
// rsc.io/quote is treated as "private" so the proxy shells out to
// `go mod download` on the host and streams the cached files back. The host's
// shell-out itself still fetches from proxy.golang.org — we're testing the
// routing and streaming, not git-credential inheritance (which is inherently
// machine-specific).
func TestE2E_PrivateModuleRoundtrip(t *testing.T) {
	bin := buildBinary(t)
	// Isolated GOMODCACHE for the proxy's child `go mod download`, so the test
	// doesn't scribble into the dev's ~/go/pkg/mod. GOPROXY is pinned to
	// proxy.golang.org to prevent the dev's `go env -w GOPROXY=...` from
	// redirecting the child anywhere unexpected (including back to ourselves).
	proxyCache := newGOMODCACHE(t)
	proxyEnv := []string{
		"GOMODCACHE=" + proxyCache,
		"GOPROXY=https://proxy.golang.org,direct",
	}
	addr, creds, certPath := startProxy(t, bin, "rsc.io/*", proxyEnv)

	clientCache := downloadThroughProxy(t, addr, creds, certPath, "rsc.io/quote@v1.5.2", nil)

	// Client cache must contain the zip — proving PrivateFetcher streamed it
	// back over HTTP.
	clientZip := filepath.Join(clientCache, "cache/download/rsc.io/quote/@v/v1.5.2.zip")
	_, err := os.Stat(clientZip)
	require.NoError(t, err, "expected %s in client cache", clientZip)

	// Proxy's own cache must also contain the zip — proving the shell-out to
	// `go mod download` actually ran on the server side.
	proxyZip := filepath.Join(proxyCache, "cache/download/rsc.io/quote/@v/v1.5.2.zip")
	_, err = os.Stat(proxyZip)
	assert.NoError(t, err, "expected %s in proxy cache (shell-out did not run)", proxyZip)
}

func buildBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "local-gomod-proxy")
	build := exec.Command("go", "build", "-o", bin, "./cmd/local-gomod-proxy")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build: %s", out)
	return bin
}

func startProxy(t *testing.T, bin, private string, extraEnv []string) (addr, creds, certPath string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = l.Addr().String()
	require.NoError(t, l.Close())

	stateDir := t.TempDir()
	certPath = filepath.Join(stateDir, "cert.pem")

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
				return addr, creds, certPath
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// downloadThroughProxy runs `go mod download <modVersion>` with GOPROXY pointed
// at the given proxy address and returns the isolated GOMODCACHE the client
// wrote into. GOPRIVATE and GONOPROXY are cleared so the client cannot bypass
// the proxy via a dev's `go env -w` settings. certPath is passed as
// SSL_CERT_FILE so the subprocess trusts the proxy's self-signed cert.
func downloadThroughProxy(t *testing.T, proxyAddr, creds, certPath, modVersion string, extraEnv []string) string {
	t.Helper()
	scratch := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o600))

	cache := newGOMODCACHE(t)

	host, _, err := net.SplitHostPort(proxyAddr)
	require.NoError(t, err)

	env := append([]string{
		fmt.Sprintf("GOPROXY=https://%s@%s/", creds, proxyAddr),
		"GOINSECURE=" + host,
		"SSL_CERT_FILE=" + certPath,
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

// newGOMODCACHE creates an isolated GOMODCACHE directory. `go mod download`
// writes read-only files into its cache, so we chmod everything back to 0700
// before cleanup — otherwise t.TempDir's RemoveAll fails on the read-only
// entries. For that reason we use os.MkdirTemp, not t.TempDir.
func newGOMODCACHE(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gomodcache-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, _ error) error {
			return os.Chmod(p, 0o700)
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}
