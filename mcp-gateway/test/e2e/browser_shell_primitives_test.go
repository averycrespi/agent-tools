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

func TestBrowserShellPrimitives(t *testing.T) {
	assertBrowserEnvironmentManifest(t)
	root := filepath.Join("..", "..")
	primitiveSource, err := os.ReadFile(filepath.Join(root, "web", "src", "primitives.tsx"))
	require.NoError(t, err)
	mainSource, err := os.ReadFile(filepath.Join(root, "web", "src", "main.tsx"))
	require.NoError(t, err)
	locationSource, err := os.ReadFile(filepath.Join(root, "web", "src", "location.ts"))
	require.NoError(t, err)
	styles, err := os.ReadFile(filepath.Join(root, "web", "src", "styles.css"))
	require.NoError(t, err)
	primitiveText := string(primitiveSource)
	mainText := string(mainSource)
	authoredText := primitiveText + mainText
	for _, symbol := range []string{"ConfirmationDialog", "ComparisonTable", "FormField", "InertJSON", "StateNotice", "StatusLabel"} {
		assert.Contains(t, primitiveText, "export function "+symbol)
	}
	for _, forbidden := range []string{"dangerouslySetInnerHTML", "innerHTML", "insertAdjacentHTML", "window.open", "navigator.clipboard", "href=", "src="} {
		assert.NotContains(t, primitiveText, forbidden)
	}
	for _, forbidden := range []string{"dangerouslySetInnerHTML", "innerHTML", "insertAdjacentHTML", "javascript:", "data:text/html", "blob:", "window.open", "navigator.clipboard", "target="} {
		assert.NotContains(t, authoredText, forbidden)
	}
	assert.Equal(t, 3, strings.Count(mainText, "href="), "shell URLs must stay in the three fixed navigation owners")
	for _, allowed := range []string{`href="#main-content"`, `href="#/overview"`, `href={item.href}`} {
		assert.Contains(t, mainText, allowed)
	}
	assert.NotContains(t, mainText, "history.replaceState")
	assert.Contains(t, string(locationSource), "window.history.replaceState")
	assert.Contains(t, primitiveText, "<code>{serialized}</code>")
	for _, required := range []string{"prefers-reduced-motion: reduce", "overflow-wrap: anywhere", ".table-region", ".skip-link", "dialog::backdrop"} {
		assert.Contains(t, string(styles), required)
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
		"version": 1, "scenario": "shell-primitives", "base_url": "http://" + harness.authority,
		"admin_bearer": harness.bearer,
	}))
	require.NoError(t, input.Close())

	result, waitErr := process.Wait()
	finished = true
	require.NoError(t, waitErr, "shell primitives browser scenario: %s", result.Stderr)
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
	assert.Equal(t, "shell_primitives_complete", event.Event)
	assert.NotEmpty(t, event.ChromiumVersion)
	assert.Equal(t, "1.62.1", event.PlaywrightVersion)
	assert.Positive(t, event.Requests)

	harness.Stop(os.Interrupt)
	assert.Len(t, harness.results, 1, "shell primitive proof must own one Gateway lifecycle")
}
