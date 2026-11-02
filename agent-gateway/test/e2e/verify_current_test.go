//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type e2eClock struct{}

func (e2eClock) Now() time.Time { return time.Now() }

func TestHistoricalSQLIntentNeedsNoManualClearanceRealBinary(t *testing.T) {
	h := newGatewayHarness(t)
	identity, err := storage.InspectClosedDatabase(t.Context(), filepath.Join(h.root, gatewaypaths.DatabaseName))
	require.NoError(t, err)
	path := filepath.Join(h.root, gatewaypaths.MutationMarkerName)
	marker := []byte(`{"installation_id":"` + identity.InstallationID + `","state":"armed"}`)
	require.NoError(t, os.WriteFile(path, marker, 0600))
	for range 2 {
		h.Start()
		h.Stop(syscall.SIGTERM)
		actual, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, marker, actual)
	}
}

func TestGenericStorageRecoveryCommandIsRemovedRealBinary(t *testing.T) {
	h := newGatewayHarness(t)
	before, err := os.ReadFile(filepath.Join(h.root, gatewaypaths.DatabaseName))
	require.NoError(t, err)
	result, err := h.runner.Run(t.Context(), h.binary, "maintenance", "verify-and-recover-storage", "--confirm", "--data-dir", h.root, "--json")
	require.Error(t, err)
	require.Equal(t, 2, result.ExitCode)
	require.Empty(t, result.Stdout)
	after, err := os.ReadFile(filepath.Join(h.root, gatewaypaths.DatabaseName))
	require.NoError(t, err)
	require.Equal(t, before, after)
}
