//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIOnlineOfflineOutputCanary(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))

	status := runOnlineCLI(t, harness, bearerPath, true, "status", "--output", "json")
	var snapshot contract.SystemStatus
	require.NoError(t, json.Unmarshal(status.Stdout, &snapshot))
	assert.Equal(t, contract.ProcessReady, snapshot.Process.State)

	invocations := runOnlineCLI(t, harness, bearerPath, true, "invocation", "list", "--limit", "1", "--output", "json")
	var page contract.InvocationPage
	require.NoError(t, json.Unmarshal(invocations.Stdout, &page))
	assert.Empty(t, page.Items)

	restore, err := harness.runner.Run(context.Background(), harness.binary, "restore", "--verify-current", "--data-dir", harness.root, "--output", "json")
	require.Error(t, err)
	assert.Equal(t, 5, restore.ExitCode)
	assert.Empty(t, restore.Stdout)
	assert.JSONEq(t, `{"status":null,"code":"gateway_running","title":"The Gateway is running. Stop it before verifying or restoring the installation.","exit_code":5,"uncertain":false}`, string(restore.Stderr))

	harness.Stop(syscall.SIGTERM)
	stopped := runOnlineCLI(t, harness, bearerPath, false, "status", "--output", "json")
	assert.Equal(t, 9, stopped.ExitCode)
	assert.Empty(t, stopped.Stdout)
	assert.Contains(t, string(stopped.Stderr), `"code":"gateway_not_running"`)
	assert.Contains(t, string(stopped.Stderr), "Start it with: mcp-gateway serve")

	for _, result := range []testutil.ProcessResult{status, invocations, restore, stopped} {
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "M4 gate must own one Gateway lifecycle")
}
