//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrontendDevelopmentControlPlane(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	repositoryRoot := frontendRepositoryRoot(t)
	workspaceBefore := trackedWorkspaceDigest(t, repositoryRoot)
	temporaryBefore := developmentTemporaryRoots(t)

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
		filepath.Join(repositoryRoot, "mcp-gateway", "web", "dev-server.ts"),
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
		"version": 1, "scenario": "development-control-plane", "base_url": "http://" + frontendAuthority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	browserResult, browserErr := browserProcess.Wait()
	browserFinished = true
	require.NoError(t, browserErr, "development control-plane browser scenario: %s", browserResult.Stderr)
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
		EventStreams      int    `json:"event_streams"`
		Mutations         int    `json:"mutations"`
		SafeReads         int    `json:"safe_reads"`
		HMRResources      int    `json:"hmr_resources"`
		CookieHostOnly    bool   `json:"cookie_host_only"`
		EpochFenced       bool   `json:"epoch_fenced"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(browserResult.Stdout))), &event))
	assert.Equal(t, "development_control_plane_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.Equal(t, 2, event.EventStreams)
	assert.Equal(t, 1, event.Mutations)
	assert.Positive(t, event.SafeReads)
	assert.Positive(t, event.HMRResources)
	assert.True(t, event.CookieHostOnly)
	assert.True(t, event.EpochFenced)

	callbackCanary := "development-callback-canary-7f1d"
	callback := harness.Request(http.MethodGet, "/oauth/callback?state="+callbackCanary+"&code="+callbackCanary, "", nil)
	callbackBody, err := io.ReadAll(callback.Body)
	require.NoError(t, err)
	require.NoError(t, callback.Body.Close())
	assert.Equal(t, http.StatusBadRequest, callback.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", callback.Header.Get("Content-Type"))
	assert.Equal(t, "default-src 'none'; frame-ancestors 'none'", callback.Header.Get("Content-Security-Policy"))
	assert.NotContains(t, string(callbackBody), callbackCanary)

	alternateOrigin := harness.Request(http.MethodGet, "/api/v1/system-status", "", map[string]string{
		"Origin": "http://" + frontendAuthority,
	})
	assert.Equal(t, http.StatusForbidden, alternateOrigin.StatusCode)
	require.NoError(t, alternateOrigin.Body.Close())
	forwarded := harness.Request(http.MethodGet, "/api/v1/system-status", "", map[string]string{
		"X-Forwarded-For": "127.0.0.1",
	})
	assert.Equal(t, http.StatusBadRequest, forwarded.StatusCode)
	require.NoError(t, forwarded.Body.Close())
	hmr := harness.Request(http.MethodGet, "/@vite/client", "", nil)
	assert.Equal(t, http.StatusNotFound, hmr.StatusCode)
	require.NoError(t, hmr.Body.Close())

	knownTemporaryRoots := make(map[string]struct{}, len(temporaryBefore))
	for _, root := range temporaryBefore {
		knownTemporaryRoots[root] = struct{}{}
	}
	for _, root := range developmentTemporaryRoots(t) {
		if _, known := knownTemporaryRoots[root]; known {
			continue
		}
		for _, secret := range []string{harness.bearer, callbackCanary} {
			assertDirectorySecretAbsent(t, root, secret)
		}
	}

	require.NoError(t, devProcess.Signal(os.Interrupt))
	devResult, devErr := devProcess.Wait()
	devFinished = true
	require.NoError(t, devErr, "development server shutdown: %s", devResult.Stderr)
	assert.False(t, devResult.StdoutTruncated)
	assert.False(t, devResult.StderrTruncated)
	assert.True(t, devResult.Cleanup.Reaped)
	assert.False(t, devResult.Cleanup.Survived)
	for _, secret := range []string{harness.bearer, callbackCanary} {
		assert.NotContains(t, string(devResult.Stdout), secret)
		assert.NotContains(t, string(devResult.Stderr), secret)
	}

	gatewayResult := harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "control-plane proof must own one Gateway lifecycle")
	for _, secret := range []string{harness.bearer, callbackCanary} {
		assert.NotContains(t, string(harness.initialization.Stdout), secret)
		assert.NotContains(t, string(harness.initialization.Stderr), secret)
		assert.NotContains(t, string(gatewayResult.Stdout), secret)
		assert.NotContains(t, string(gatewayResult.Stderr), secret)
		assertDirectorySecretAbsent(t, harness.root, secret)
	}
	assert.Equal(t, workspaceBefore, trackedWorkspaceDigest(t, repositoryRoot))
	assert.Equal(t, temporaryBefore, developmentTemporaryRoots(t))
}

func assertDirectorySecretAbsent(t *testing.T, root, secret string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assert.NotContains(t, string(contents), secret, path)
		return nil
	}))
}
