//go:build e2e && browser

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserServerCreateUpdate(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	// Export the complete retained graph from an independently initialized running
	// source, then stop it before giving the browser its fresh receiving Gateway.
	source := newGatewayHarness(t)
	source.Start()
	seed := bundleCLIGraph()
	seed.Resources[2].Spec = json.RawMessage(strings.ReplaceAll(string(seed.Resources[2].Spec), `"/decimal":1.0`, `"/decimal":1.0,"/exponent":1e+2`))
	seeded := source.adminSnapshot(http.MethodPost, contract.ConfigImportPath, mustMarshalConfigurationE2E(t, seed))
	require.Equal(t, http.StatusCreated, seeded.StatusCode)
	exported := source.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, http.StatusOK, exported.StatusCode)
	require.NotContains(t, string(exported.Body), source.bearer)
	source.Stop(os.Interrupt)
	assert.Len(t, source.results, 1)
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
		"version": 1, "scenario": "server-create-update", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer, "configuration": string(exported.Body),
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "server create/update browser scenario: %s", result.Stderr)
	assert.Empty(t, result.Stderr)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.NotContains(t, string(result.Stdout), harness.bearer)
	assert.NotContains(t, string(result.Stderr), harness.bearer)

	var event struct {
		Event             string   `json:"event"`
		ChromiumVersion   string   `json:"chromium_version"`
		PlaywrightVersion string   `json:"playwright_version"`
		Requests          int      `json:"requests"`
		HeaderScreenshots []string `json:"header_screenshots"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	assert.Equal(t, "server_create_update_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)
	// Mocked editor states now belong to the frontend-only suite. This owner
	// retains real API creation, header updates and persistence after reload.
	assert.Len(t, event.HeaderScreenshots, 15)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "receiving fixture owns one Gateway lifecycle, separate from the closed source")
}
