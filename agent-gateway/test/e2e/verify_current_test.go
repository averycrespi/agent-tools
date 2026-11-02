//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyCurrentRealBinary(t *testing.T) {
	ctx := context.Background()
	binary := gatewayBinary(t)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, composition.InitializeTraffic(ctx, ownership, store, "01ARZ3NDEKTSV4RRFFQ69G5FAW"))
	require.NoError(t, store.Close())
	marker := `{"installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","state":"armed"}` + "\n"
	require.NoError(t, os.WriteFile(ownership.Layout().MutationMarker, []byte(marker), 0o600))
	require.NoError(t, ownership.Close())
	runner, err := testutil.NewBinaryRunner(10*time.Second, 4096)
	require.NoError(t, err)
	result, err := runner.Run(ctx, binary, "maintenance", "verify-and-recover-storage", "--confirm", "--data-dir", root, "--json")
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.JSONEq(t, `{"ok":true,"operation":"verify-and-recover-storage","installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","revision":"0"}`, string(result.Stdout))
	assertMaintenancePlan(t, result.Stderr, "verify-and-recover-storage")
	assert.False(t, result.StdoutTruncated)
	_, err = os.Lstat(filepath.Join(root, gatewaypaths.MutationMarkerName))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

type e2eClock struct{}

func (e2eClock) Now() time.Time { return time.Now() }

func TestVerifyCurrentRealBinaryRefusesRunningGateway(t *testing.T) {
	ctx := context.Background()
	binary := gatewayBinary(t)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	runner, err := testutil.NewBinaryRunner(10*time.Second, 4096)
	require.NoError(t, err)
	result, err := runner.Run(ctx, binary, "maintenance", "verify-and-recover-storage", "--confirm", "--data-dir", root, "--json")
	assert.Error(t, err)
	assert.Equal(t, 5, result.ExitCode)
	assert.Empty(t, result.Stdout)
	assert.Contains(t, string(result.Stderr), `"code":"gateway_running"`)
	assert.Contains(t, string(result.Stderr), "Stop the selected installation")
}
