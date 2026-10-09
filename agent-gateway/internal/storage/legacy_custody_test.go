package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestLegacyCustodyRefusesOpenAndMaintenanceWithoutDiscardingEvidence(t *testing.T) {
	for _, state := range []string{"legacy", "retained", "uncertain", "reappeared", "incomplete", "deleted"} {
		t.Run(state, func(t *testing.T) {
			owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			const installation = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
			store, err := Initialize(t.Context(), owner, installation)
			require.NoError(t, err)
			handle := "mgw_kg1_" + strings.Repeat("A", 43)
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				if state == "legacy" {
					_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) VALUES(?,?,'http_ca','legacy')`, handle, installation)
					return err
				}
				if _, err := tx.Exec(`INSERT INTO native_cleanup(handle,owner,kind,revision,chunks) VALUES(?,?,'http_ca',1,1)`, handle, installation); err != nil {
					return err
				}
				for item := -1; item < 117; item++ {
					if state == "incomplete" && item == 116 {
						continue
					}
					disposition := state
					if state == "incomplete" {
						disposition = "deleted"
					}
					if _, err := tx.Exec(`INSERT INTO native_cleanup_items(handle,item,state) VALUES(?,?,?)`, handle, item, disposition); err != nil {
						return err
					}
				}
				return nil
			}))
			require.NoError(t, store.Close())
			before, err := os.ReadFile(owner.Layout().Database)
			require.NoError(t, err)
			visited := false
			_, err = InspectMaintenance(t.Context(), owner, func(*sql.Tx) error { visited = true; return nil })
			if state == "deleted" {
				require.NoError(t, err)
				require.True(t, visited)
			} else {
				require.ErrorIs(t, err, ErrLegacyCustody)
				require.False(t, visited)
			}
			reopened, err := Open(t.Context(), owner)
			if state == "deleted" {
				require.NoError(t, err)
				require.NoError(t, reopened.Close())
			} else {
				require.ErrorIs(t, err, ErrLegacyCustody)
				require.Nil(t, reopened)
			}
			stage := filepath.Join(owner.Layout().Root, "legacy-restore-stage.db")
			require.NoError(t, os.WriteFile(stage, before, 0o600))
			replacement, err := OpenReplacement(t.Context(), owner, stage)
			if state == "deleted" {
				require.NoError(t, err)
				require.NoError(t, replacement.Close())
			} else {
				if replacement != nil {
					require.NoError(t, replacement.Close())
				}
				require.ErrorIs(t, err, ErrLegacyCustody, "restore staging must not discard historical authority")
			}
			stageAfter, err := os.ReadFile(stage)
			require.NoError(t, err)
			require.Equal(t, before, stageAfter)
			after, err := os.ReadFile(owner.Layout().Database)
			require.NoError(t, err)
			require.Equal(t, before, after, "refusal must preserve references and cleanup evidence")
			_, err = os.Lstat(filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName))
			require.True(t, os.IsNotExist(err), "refusal must not create replacement key material")
		})
	}
}
