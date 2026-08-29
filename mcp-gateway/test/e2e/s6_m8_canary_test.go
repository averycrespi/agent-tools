//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6BrowserM8Canary(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
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
		"version": 1, "scenario": "m8-canary", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())
	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "M8 browser canary: %s", result.Stderr)
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
		Destinations      int    `json:"destinations"`
		Mutations         int    `json:"mutations"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "m8_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.Equal(t, 4, event.Destinations)
	assert.Zero(t, event.Mutations)
	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "M8 canary must own one Gateway lifecycle")
}
