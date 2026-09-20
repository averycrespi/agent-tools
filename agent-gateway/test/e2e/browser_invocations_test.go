//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserInvocations(t *testing.T) {
	testInvocationBrowser(t, "invocations")
}

func TestBrowserInvocationHistory(t *testing.T) {
	testInvocationBrowser(t, "invocation-history")
}

func testInvocationBrowser(t *testing.T, scenario string) {
	t.Helper()
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
		"version": 1, "scenario": scenario, "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "invocations browser scenario: %s", result.Stderr)
	assert.Empty(t, result.Stderr)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.NotContains(t, string(result.Stdout), harness.bearer)
	assert.NotContains(t, string(result.Stderr), harness.bearer)

	var event struct {
		Event              string   `json:"event"`
		ChromiumVersion    string   `json:"chromium_version"`
		PlaywrightVersion  string   `json:"playwright_version"`
		Requests           int      `json:"requests"`
		ListReads          int      `json:"list_reads"`
		ContinuationReads  int      `json:"continuation_reads"`
		ItemReads          int      `json:"item_reads"`
		HistoryScreenshots []string `json:"history_screenshots"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, scenario+"_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	if scenario == "invocations" {
		assert.GreaterOrEqual(t, event.ListReads, 4)
		assert.Positive(t, event.ContinuationReads)
		assert.Equal(t, 8, event.ItemReads)
		assert.GreaterOrEqual(t, len(event.HistoryScreenshots), 20)
	} else {
		assert.GreaterOrEqual(t, len(event.HistoryScreenshots), 30)
	}
	t.Logf("History visual artifacts: %v", event.HistoryScreenshots)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "each invocation scenario must own one Gateway lifecycle")
}
