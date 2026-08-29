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

func TestBrowserServerCatalogReads(t *testing.T) {
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
		"version": 1, "scenario": "server-catalog-reads", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "server/catalog browser scenario: %s", result.Stderr)
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
		ServerReads       int    `json:"server_reads"`
		DescriptorReads   int    `json:"descriptor_reads"`
		CatalogReads      int    `json:"catalog_reads"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "server_catalog_reads_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.GreaterOrEqual(t, event.ServerReads, 4)
	assert.GreaterOrEqual(t, event.DescriptorReads, 4)
	assert.GreaterOrEqual(t, event.CatalogReads, 3)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "T23 must own one Gateway lifecycle")
}
