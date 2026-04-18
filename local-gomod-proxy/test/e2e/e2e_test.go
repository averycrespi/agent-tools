//go:build e2e

package e2e

import (
	"context"
	"fmt"
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
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "local-gomod-proxy")
	build := exec.Command("go", "build", "-o", bin, "./cmd/local-gomod-proxy")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build: %s", out)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxyCmd := exec.CommandContext(ctx, bin, "serve", "--addr", addr, "--private", "example.invalid/private/*")
	proxyCmd.Stdout = os.Stderr
	proxyCmd.Stderr = os.Stderr
	require.NoError(t, proxyCmd.Start())
	t.Cleanup(func() { cancel(); _ = proxyCmd.Wait() })

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

	scratch := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o600))
	// Use os.MkdirTemp instead of t.TempDir so we can chmod before removal —
	// go mod download writes read-only files into GOMODCACHE.
	gomodcache, err := os.MkdirTemp("", "gomodcache-*")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = filepath.WalkDir(gomodcache, func(p string, _ os.DirEntry, _ error) error {
			return os.Chmod(p, 0o700)
		})
		_ = os.RemoveAll(gomodcache)
	})
	download := exec.Command("go", "mod", "download", "-x", "rsc.io/quote@v1.5.2")
	download.Dir = scratch
	download.Env = append(os.Environ(),
		fmt.Sprintf("GOPROXY=http://%s/", addr),
		"GOSUMDB=off",
		"GOMODCACHE="+gomodcache,
	)
	out, err = download.CombinedOutput()
	require.NoError(t, err, "download: %s", out)

	zipPath := filepath.Join(gomodcache, "cache/download/rsc.io/quote/@v/v1.5.2.zip")
	_, err = os.Stat(zipPath)
	assert.NoError(t, err, "expected %s to exist", zipPath)
}
