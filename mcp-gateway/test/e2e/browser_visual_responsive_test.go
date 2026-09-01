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

func TestBrowserVisualResponsiveMatrix(t *testing.T) {
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
		"version": 1, "scenario": "visual-responsive-matrix", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "visual browser scenario: %s", result.Stderr)
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
		Inventory         int    `json:"inventory"`
		States            int    `json:"states"`
		Rubric            int    `json:"rubric"`
		Screenshots       []struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		} `json:"screenshots"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "visual_responsive_matrix_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.Equal(t, 48, event.Inventory)
	assert.Equal(t, 10, event.States)
	assert.Equal(t, 6, event.Rubric)
	require.Len(t, event.Screenshots, 2)
	for _, screenshot := range event.Screenshots {
		assert.NotEmpty(t, screenshot.ID)
		assert.Regexp(t, `^[0-9a-f]{64}$`, screenshot.SHA256)
	}
	assert.NotEqual(t, event.Screenshots[0].SHA256, event.Screenshots[1].SHA256)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "visual proof must own one Gateway lifecycle")
}
