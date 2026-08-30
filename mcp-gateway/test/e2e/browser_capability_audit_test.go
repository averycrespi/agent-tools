//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserCapabilityAudit(t *testing.T) {
	rows := contract.ControlPlaneCapabilityManifest()
	require.Len(t, rows, 31)
	ids, scenarios := map[string]struct{}{}, map[string]struct{}{}
	mechanics := strings.Builder{}
	for _, row := range rows {
		require.NotEmpty(t, row.ID)
		require.NotEmpty(t, row.WebControl)
		require.Equal(t, "browser."+row.ID, row.WebScenario)
		_, duplicateID := ids[row.ID]
		_, duplicateScenario := scenarios[row.WebScenario]
		require.False(t, duplicateID)
		require.False(t, duplicateScenario)
		ids[row.ID], scenarios[row.WebScenario] = struct{}{}, struct{}{}
		mechanics.WriteString(" " + row.Mechanics)
	}
	for _, marker := range []string{"confirmation", "one-time sink", "idempotency key", "ETag", "cursor/limit", "no replay"} {
		assert.Contains(t, mechanics.String(), marker)
	}
	lifecycle := contract.ControlPlaneLifecycleManifest()
	require.Len(t, lifecycle, 8)
	for _, row := range lifecycle[4:] {
		assert.Empty(t, row.WebScenario, "CLI/offline capability must have no web owner")
	}

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
		"version": 1, "scenario": "capability-audit", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())
	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "capability audit browser scenario: %s", result.Stderr)
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
		EventStreams      int    `json:"event_streams"`
		Mutations         int    `json:"mutations"`
		Destinations      int    `json:"destinations"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "capability_audit_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	assert.GreaterOrEqual(t, event.EventStreams, 2)
	assert.Zero(t, event.Mutations)
	assert.Equal(t, 2, event.Destinations)
	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "capability audit must own one Gateway lifecycle")
}
