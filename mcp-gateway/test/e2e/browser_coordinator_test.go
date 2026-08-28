//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6BrowserCoordinator(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	assertOrdinaryBinaryNeedsNoNode(t)
	harness := newGatewayHarness(t)
	harness.Start()

	runner, err := testutil.NewBinaryRunner(15*time.Second, 16*1024)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process, input, err := runner.StartWithInputPipe(ctx, "node", browserBridgePath(t))
	require.NoError(t, err)
	payload := map[string]any{
		"version": 1, "scenario": "shell-load", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}
	require.NoError(t, json.NewEncoder(input).Encode(payload))
	require.NoError(t, input.Close())

	select {
	case <-process.StdoutReady():
	case <-time.After(10 * time.Second):
		_ = process.Stop()
		result, waitErr := process.Wait()
		t.Fatalf("browser bridge did not load shell: wait=%v stderr=%s", waitErr, result.Stderr)
	}
	require.NoError(t, process.Stop())
	result, waitErr := process.Wait()
	require.Error(t, waitErr)
	assert.ErrorContains(t, waitErr, "test process stopped")
	assert.Equal(t, "{\"event\":\"shell_loaded\"}\n", string(result.Stdout))
	assert.Empty(t, result.Stderr)
	assert.True(t, result.Cleanup.TermSent)
	assert.True(t, result.Cleanup.KillSent)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.NotContains(t, string(result.Stdout), harness.bearer)
	assert.NotContains(t, string(result.Stderr), harness.bearer)

	harness.Stop(os.Interrupt)
}

func browserBridgePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(browserWebRoot(t), "tests", "browser-coordinator.ts")
}

func browserWebRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(source), "..", "..", "web")
}

func assertOrdinaryBinaryNeedsNoNode(t *testing.T) {
	t.Helper()
	temporaryRoot := t.TempDir()
	ordinaryBinary := filepath.Join(temporaryRoot, "mcp-gateway")
	goBinary, err := exec.LookPath("go")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	require.NoError(t, err)
	moduleRoot := filepath.Dir(browserWebRoot(t))
	built, err := runner.Run(context.Background(), goBinary, "-C", moduleRoot, "build", "-o", ordinaryBinary, "./cmd/mcp-gateway")
	require.NoError(t, err, "ordinary build: %s", built.Stderr)
	envBinary, err := exec.LookPath("env")
	require.NoError(t, err)
	result, err := runner.RunInDir(context.Background(), temporaryRoot, envBinary, "-i", "PATH=/nonexistent", "HOME="+temporaryRoot, ordinaryBinary, "--help")
	require.NoError(t, err, "ordinary binary without Node: %s", result.Stderr)
	assert.Contains(t, string(result.Stdout), "mcp-gateway")
	assert.NoDirExists(t, filepath.Join(temporaryRoot, "node_modules"))
}

func assertBrowserEnvironmentManifest(t *testing.T) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(browserWebRoot(t), "environments.json"))
	require.NoError(t, err)
	var manifest struct {
		SchemaVersion     int    `json:"schema_version"`
		PlaywrightVersion string `json:"playwright_version"`
		Cells             []struct {
			ID               string `json:"id"`
			Browser          string `json:"browser"`
			Availability     string `json:"availability"`
			AcceptanceClass  string `json:"acceptance_class"`
			UnavailableClass string `json:"unavailable_class"`
		} `json:"cells"`
	}
	require.NoError(t, json.Unmarshal(contents, &manifest))
	assert.Equal(t, 1, manifest.SchemaVersion)
	assert.Equal(t, "1.62.1", manifest.PlaywrightVersion)
	require.Len(t, manifest.Cells, 5)
	assert.Equal(t, "linux-chromium", manifest.Cells[0].ID)
	assert.Equal(t, "available", manifest.Cells[0].Availability)
	assert.Equal(t, "blocking", manifest.Cells[0].AcceptanceClass)
	for _, cell := range manifest.Cells[1:] {
		assert.Equal(t, "blocking_when_available", cell.AcceptanceClass, cell.ID)
		assert.Equal(t, "additive", cell.UnavailableClass, cell.ID)
	}
}
