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
	"github.com/stretchr/testify/require"
)

func TestBrowserHTTPCredentials(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	harness := newGatewayHarness(t)
	harness.Start()
	runner, err := testutil.NewBinaryRunner(75*time.Second, 32*1024)
	require.NoError(t, err)
	process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	finished := false
	t.Cleanup(func() {
		if !finished {
			require.NoError(t, process.Stop())
			result, _ := process.Wait()
			require.True(t, result.Cleanup.Reaped)
			require.False(t, result.Cleanup.Survived)
		}
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{"version": 1, "scenario": "http-credentials", "base_url": "http://" + harness.authority, "admin_bearer": harness.bearer}))
	require.NoError(t, input.Close())
	result, err := process.Wait()
	finished = true
	require.NoError(t, err, "HTTP credential browser scenario: %s", result.Stderr)
	require.Empty(t, result.Stderr)
	require.False(t, result.StdoutTruncated)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.NotContains(t, string(result.Stdout), harness.bearer)
	var event struct {
		Event       string `json:"event"`
		Requests    int    `json:"requests"`
		Screenshots string `json:"screenshots"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	require.Equal(t, "http_credentials_complete", event.Event)
	require.Positive(t, event.Requests)
	require.NotEmpty(t, event.Screenshots)
	t.Logf("HTTP credential screenshots: %s", event.Screenshots)
	harness.Stop(os.Interrupt)
	require.Len(t, harness.results, 1)
}
