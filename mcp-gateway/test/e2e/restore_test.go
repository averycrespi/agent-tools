//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreVerifyCurrentRealBinary(t *testing.T) {
	ctx := context.Background()
	binary := buildGateway(t, ctx)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	marker := `{"installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","state":"armed"}` + "\n"
	require.NoError(t, os.WriteFile(ownership.Layout().MutationMarker, []byte(marker), 0o600))
	require.NoError(t, ownership.Close())

	runner, err := testutil.NewBinaryRunner(10*time.Second, 4096)
	require.NoError(t, err)
	result, err := runner.Run(ctx, binary, "restore", "--verify-current", "--data-dir", root)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.JSONEq(t, `{"ok":true,"operation":"restore","mode":"verify_current","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"0"}`, string(result.Stdout))
	assert.Empty(t, result.Stderr)
	assert.False(t, result.StdoutTruncated)
	_, err = os.Lstat(filepath.Join(root, gatewaypaths.MutationMarkerName))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRestoreVerifyCurrentRealBinaryRefusesRunningGateway(t *testing.T) {
	ctx := context.Background()
	binary := buildGateway(t, ctx)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()

	runner, err := testutil.NewBinaryRunner(10*time.Second, 4096)
	require.NoError(t, err)
	result, err := runner.Run(ctx, binary, "restore", "--verify-current", "--data-dir", root)
	assert.Error(t, err)
	assert.Equal(t, 1, result.ExitCode)
	assert.JSONEq(t, `{"ok":false,"operation":"restore","code":"gateway_running"}`, string(result.Stdout))
	assert.Empty(t, result.Stderr)
}

func buildGateway(t *testing.T, ctx context.Context) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	binary := filepath.Join(t.TempDir(), "mcp-gateway")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	require.NoError(t, err)
	result, err := runner.Run(ctx, "go", "-C", moduleRoot, "build", "-o", binary, "./cmd/mcp-gateway")
	require.NoError(t, err, "build stderr: %s", result.Stderr)
	return binary
}
