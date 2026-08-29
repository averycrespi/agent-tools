//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6BrowserCross(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	harness := newGatewayHarness(t)
	harness.Start()

	for _, browser := range []string{"firefox", "webkit"} {
		t.Run(browser, func(t *testing.T) {
			runner, err := testutil.NewBinaryRunner(20*time.Second, 16*1024)
			require.NoError(t, err)
			process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
			require.NoError(t, err)
			_, err = input.WriteString(mustJSONLine(t, map[string]any{
				"version": 1, "scenario": "shell-load", "browser_kind": browser,
				"base_url": "http://" + harness.authority, "admin_bearer": harness.bearer,
			}))
			require.NoError(t, err)
			require.NoError(t, input.Close())
			select {
			case <-process.StdoutReady():
			case <-time.After(15 * time.Second):
				_ = process.Stop()
				result, waitErr := process.Wait()
				t.Fatalf("%s shell did not load: wait=%v stderr=%s", browser, waitErr, result.Stderr)
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
		})
	}

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "cross-browser proof must share one Gateway lifecycle")
}

func mustJSONLine(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded) + "\n"
}
