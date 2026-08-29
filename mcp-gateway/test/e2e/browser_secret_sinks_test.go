//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserSecretSinks(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	root := filepath.Join("..", "..")
	controllerSource, err := os.ReadFile(filepath.Join(root, "web", "src", "sinks.ts"))
	require.NoError(t, err)
	uiSource, err := os.ReadFile(filepath.Join(root, "web", "src", "sinks-ui.tsx"))
	require.NoError(t, err)
	controllerText := string(controllerSource)
	uiText := string(uiSource)
	for _, required := range []string{"prepareOneTime", "prepareOAuth", "clearForNavigation", "registerProtectedState", "isCurrent"} {
		assert.Contains(t, controllerText, required)
	}
	for _, required := range []string{"navigator.clipboard.writeText", `window.open(url, "_blank", "noopener,noreferrer")`, "selection.removeAllRanges", "autocomplete=\"off\""} {
		assert.Contains(t, uiText, required)
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "indexedDB", "caches.", "serviceWorker", "dangerouslySetInnerHTML", "innerHTML", "insertAdjacentHTML", "download=", "href=", "value={secret}"} {
		assert.NotContains(t, controllerText+uiText, forbidden)
	}
	entries, err := os.ReadDir(filepath.Join(root, "web", "src"))
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".ts" && filepath.Ext(entry.Name()) != ".tsx") {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(root, "web", "src", entry.Name()))
		require.NoError(t, readErr)
		if strings.Contains(string(contents), "navigator.clipboard") || strings.Contains(string(contents), "window.open") {
			assert.Equal(t, "sinks-ui.tsx", entry.Name(), "browser publication must have one production owner")
		}
	}

	harness := newGatewayHarness(t)
	harness.Start()
	runner, err := testutil.NewBinaryRunner(75*time.Second, 32*1024)
	require.NoError(t, err)
	process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		require.NoError(t, process.Stop())
		result, _ := process.Wait()
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{
		"version": 1, "scenario": "secret-sinks", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "browser secret sink scenario: %s", result.Stderr)
	assert.Empty(t, result.Stderr)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.NotContains(t, string(result.Stdout), harness.bearer)
	assert.NotContains(t, string(result.Stderr), harness.bearer)

	var event struct {
		Event             string `json:"event"`
		ChromiumVersion   string `json:"chromium_version"`
		PlaywrightVersion string `json:"playwright_version"`
		Requests          int    `json:"requests"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "secret_sinks_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "browser secret sink proof must own one Gateway lifecycle")
}
