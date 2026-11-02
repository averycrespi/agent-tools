package invocation

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestTrafficInitializationDoesNotExtractOrDeleteHistoricalEvidence(t *testing.T) {
	ctx := context.Background()
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "root"))
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	control, err := storage.Initialize(ctx, owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	repository, err := NewRepository(control, &repositoryClock{now: invocationTestTime}, entropyBytes(128))
	require.NoError(t, err)
	require.NoError(t, repository.Insert(ctx, trafficPrepared(1)))
	config := DefaultTrafficConfig()
	config.BudgetBytes = 8 << 20
	generation := invocationID(90)
	require.ErrorIs(t, InitializeTraffic(ctx, owner, control, generation, config), ErrInvalidState)
	selected, err := control.SelectedTraffic(ctx)
	require.NoError(t, err)
	require.Empty(t, selected)
	_, err = os.Lstat(filepath.Join(owner.Layout().Root, "traffic-"+generation+".db"))
	require.ErrorIs(t, err, os.ErrNotExist)
	var retained int
	require.NoError(t, control.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations WHERE id=?`, invocationID(1)).Scan(&retained)
	}))
	require.Equal(t, 1, retained)
	require.ErrorIs(t, InitializeTraffic(ctx, owner, control, invocationID(91), config), ErrInvalidState)
}
