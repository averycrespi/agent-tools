package invocation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficStoppedMigrationPreservesEvidenceAndSelection(t *testing.T) {
	for _, crash := range []string{"", "published", "selected"} {
		t.Run(crash, func(t *testing.T) {
			ctx := context.Background()
			owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "root"))
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			control, err := storage.Initialize(ctx, owner, invocationTestInstallationID)
			require.NoError(t, err)
			defer func() { require.NoError(t, control.Close()) }()
			repository, err := NewRepository(control, &repositoryClock{now: invocationTestTime}, entropyBytes(128))
			require.NoError(t, err)
			for _, id := range []int{1, 2, 3} {
				require.NoError(t, repository.Insert(ctx, trafficPrepared(id)))
			}
			require.NoError(t, repository.AnnotateTerminal(ctx, invocationID(2), contract.TerminalSucceeded))
			require.NoError(t, control.Mutate(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `DELETE FROM invocations WHERE insertion_sequence IN (1,3)`)
				return e
			}))
			config := DefaultTrafficConfig()
			config.BudgetBytes = 8 << 20
			err = migrateTraffic(ctx, owner, control, invocationID(90), config, func(point string) error {
				if point == crash {
					return errors.New("interrupted")
				}
				return nil
			})
			if crash == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			selected, err := control.SelectedTraffic(ctx)
			require.NoError(t, err)
			if crash == "published" {
				assert.Empty(t, selected)
			} else {
				assert.Equal(t, invocationID(90), selected)
			}
			traffic, err := OpenTraffic(ctx, owner, invocationTestInstallationID, invocationID(90), config)
			require.NoError(t, err)
			defer func() { require.NoError(t, traffic.Close()) }()
			history, err := traffic.History(ctx, 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			assert.Equal(t, int64(3), history.HighWater)
			assert.Equal(t, int64(2), history.Pruning)
			assert.Equal(t, invocationID(2), history.Records[0].InvocationID)
			require.NotNil(t, history.Records[0].TerminalClass)
			assert.Equal(t, contract.TerminalSucceeded, *history.Records[0].TerminalClass)
			assert.Empty(t, traffic.pins)
		})
	}
}
