//go:build e2e && browser

package e2e

import (
	"bufio"
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

func TestS6BrowserProtocol(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	runner, err := testutil.NewBinaryRunner(60*time.Second, 32*1024)
	require.NoError(t, err)
	process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	browserFinished := false
	t.Cleanup(func() {
		if browserFinished {
			return
		}
		require.NoError(t, process.Stop())
		result, _ := process.Wait()
		require.True(t, result.Cleanup.Reaped)
		require.False(t, result.Cleanup.Survived)
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{
		"version": 1, "scenario": "browser-protocol", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))

	select {
	case <-process.StdoutReady():
	case <-time.After(30 * time.Second):
		_ = process.Stop()
		result, waitErr := process.Wait()
		t.Fatalf("browser protocol did not request restart: wait=%v stderr=%s", waitErr, result.Stderr)
	}
	harness.Restart()
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{"version": 1, "event": "gateway_restarted"}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	browserFinished = true
	require.NoError(t, waitErr, "browser protocol: %s", result.Stderr)
	assert.Empty(t, result.Stderr)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.NotContains(t, string(result.Stdout), harness.bearer)
	assert.NotContains(t, string(result.Stderr), harness.bearer)

	var events []struct {
		Event             string `json:"event"`
		ChromiumVersion   string `json:"chromium_version"`
		PlaywrightVersion string `json:"playwright_version"`
		Requests          int    `json:"requests"`
	}
	scanner := bufio.NewScanner(strings.NewReader(string(result.Stdout)))
	for scanner.Scan() {
		var event struct {
			Event             string `json:"event"`
			ChromiumVersion   string `json:"chromium_version"`
			PlaywrightVersion string `json:"playwright_version"`
			Requests          int    `json:"requests"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &event))
		events = append(events, event)
	}
	require.NoError(t, scanner.Err())
	require.Len(t, events, 2)
	assert.Equal(t, "restart_requested", events[0].Event)
	assert.Equal(t, "protocol_complete", events[1].Event)
	assert.NotEmpty(t, events[1].ChromiumVersion)
	assert.Equal(t, "1.62.1", events[1].PlaywrightVersion)
	assert.Positive(t, events[1].Requests)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 2, "protocol proof must own exactly two Gateway lifecycles")
}
