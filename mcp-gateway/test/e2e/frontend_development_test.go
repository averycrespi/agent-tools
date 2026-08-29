//go:build e2e && browser

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrontendDevelopmentLiveReload(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	repositoryRoot := frontendRepositoryRoot(t)
	workspaceBefore := trackedWorkspaceDigest(t, repositoryRoot)
	staticBefore := directoryDigest(t, filepath.Join(repositoryRoot, "mcp-gateway", "internal", "api", "static"))
	temporaryBefore := developmentTemporaryRoots(t)

	fixtureRoot := filepath.Join(t.TempDir(), "frontend-development")
	fixtureWeb := filepath.Join(fixtureRoot, "web")
	copyDirectory(t, filepath.Join(repositoryRoot, "mcp-gateway", "web"), fixtureWeb)
	require.NoError(t, os.Symlink(filepath.Join(repositoryRoot, "node_modules"), filepath.Join(fixtureRoot, "node_modules")))

	harness := newGatewayHarness(t)
	harness.Start()
	frontendAuthority := unusedAuthority(t)
	envBinary, err := exec.LookPath("env")
	require.NoError(t, err)
	devRunner, err := testutil.NewBinaryRunner(90*time.Second, 64*1024)
	require.NoError(t, err)
	devProcess, err := devRunner.Start(
		context.Background(),
		envBinary,
		"MCP_GATEWAY_UI_LISTEN="+frontendAuthority,
		"MCP_GATEWAY_UI_GATEWAY=http://"+harness.authority,
		"node",
		filepath.Join(fixtureWeb, "dev-server.ts"),
	)
	require.NoError(t, err)
	devFinished := false
	t.Cleanup(func() {
		if devFinished {
			return
		}
		require.NoError(t, devProcess.Stop())
		result, _ := devProcess.Wait()
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	select {
	case <-devProcess.StdoutReady():
	case <-time.After(10 * time.Second):
		_ = devProcess.Stop()
		result, waitErr := devProcess.Wait()
		t.Fatalf("development server did not become ready: wait=%v stderr=%s", waitErr, result.Stderr)
	}
	require.NoError(t, devProcess.Signal(syscall.Signal(0)))
	devPID, err := devProcess.PID()
	require.NoError(t, err)
	assert.Positive(t, devPID)

	browserRunner, err := testutil.NewBinaryRunner(75*time.Second, 64*1024)
	require.NoError(t, err)
	browserProcess, input, err := browserRunner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	browserFinished := false
	t.Cleanup(func() {
		if browserFinished {
			return
		}
		require.NoError(t, browserProcess.Stop())
		result, _ := browserProcess.Wait()
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{
		"version": 1, "scenario": "development-live-reload", "base_url": "http://" + frontendAuthority,
		"admin_bearer": harness.bearer, "fixture_root": fixtureWeb,
	}))
	require.NoError(t, input.Close())

	browserResult, browserErr := browserProcess.Wait()
	browserFinished = true
	require.NoError(t, browserErr, "development live reload browser scenario: %s", browserResult.Stderr)
	assert.Empty(t, browserResult.Stderr)
	assert.False(t, browserResult.StdoutTruncated)
	assert.False(t, browserResult.StderrTruncated)
	assert.True(t, browserResult.Cleanup.Reaped)
	assert.False(t, browserResult.Cleanup.Survived)
	assert.NotContains(t, string(browserResult.Stdout), harness.bearer)
	assert.NotContains(t, string(browserResult.Stderr), harness.bearer)

	var event struct {
		Event             string `json:"event"`
		ChromiumVersion   string `json:"chromium_version"`
		PlaywrightVersion string `json:"playwright_version"`
		Requests          int    `json:"requests"`
		Navigations       int    `json:"navigations"`
		Bootstraps        int    `json:"bootstraps"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(browserResult.Stdout))), &event))
	assert.Equal(t, "development_live_reload_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.Equal(t, 1, event.Navigations)
	assert.Equal(t, 1, event.Bootstraps)
	require.NoError(t, devProcess.Signal(syscall.Signal(0)), "dev server must retain its original process")
	observedPID, err := devProcess.PID()
	require.NoError(t, err)
	assert.Equal(t, devPID, observedPID, "live edits must not restart the dev server")

	require.NoError(t, devProcess.Signal(os.Interrupt))
	devResult, devErr := devProcess.Wait()
	devFinished = true
	require.NoError(t, devErr, "development server shutdown: %s", devResult.Stderr)
	assert.False(t, devResult.StdoutTruncated)
	assert.False(t, devResult.StderrTruncated)
	assert.True(t, devResult.Cleanup.Reaped)
	assert.False(t, devResult.Cleanup.Survived)
	assert.NotContains(t, string(devResult.Stdout), harness.bearer)
	assert.NotContains(t, string(devResult.Stderr), harness.bearer)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "live reload proof must own one Gateway lifecycle")
	require.NoError(t, os.RemoveAll(fixtureRoot))
	assert.NoDirExists(t, fixtureRoot)
	assert.Equal(t, workspaceBefore, trackedWorkspaceDigest(t, repositoryRoot))
	assert.Equal(t, staticBefore, directoryDigest(t, filepath.Join(repositoryRoot, "mcp-gateway", "internal", "api", "static")))
	assert.Equal(t, temporaryBefore, developmentTemporaryRoots(t))
}

func frontendRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func copyDirectory(t *testing.T, source, destination string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		input, err := os.Open(path) //nolint:gosec // The source tree is repository-owned test input.
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()) //nolint:gosec // The target is an isolated test root.
		if err != nil {
			return errors.Join(err, input.Close())
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Close(), input.Close())
	}))
}

func trackedWorkspaceDigest(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "-C", root, "ls-files", "-z")
	output, err := command.Output()
	require.NoError(t, err)
	paths := strings.Split(string(output), "\x00")
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		if path == "" {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, readErr, path)
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write(contents)
	}
	status := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all")
	statusOutput, err := status.Output()
	require.NoError(t, err)
	_, _ = hash.Write(statusOutput)
	return hex.EncodeToString(hash.Sum(nil))
}

func directoryDigest(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write(contents)
		return nil
	}))
	return hex.EncodeToString(hash.Sum(nil))
}

func developmentTemporaryRoots(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "mcp-gateway-ui-development-*"))
	require.NoError(t, err)
	sort.Strings(matches)
	return matches
}
