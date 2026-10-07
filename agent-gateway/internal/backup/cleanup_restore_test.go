package backup

import (
	"crypto/rand"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEncryptedRestorePreservesNewerNativeCleanupEvidence(t *testing.T) {
	t.Parallel()
	for _, backupHasInventory := range []bool{false, true} {
		t.Run(map[bool]string{false: "newer-inventory", true: "newer-dispositions"}[backupHasInventory], func(t *testing.T) {
			ctx := audit.WithSystem(t.Context())
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			handles := make([]keyring.Handle, 2)
			for i := range handles {
				handles[i], err = keyring.NewHandle(rand.Reader)
				require.NoError(t, err)
			}
			insert := func(handle keyring.Handle, state string) {
				require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
					if _, err := tx.Exec(`INSERT INTO native_cleanup(handle,owner,kind,revision,chunks) VALUES(?,?,'http_ca',1,1)`, string(handle), backupTestInstallationID); err != nil {
						return err
					}
					for item := -1; item < 117; item++ {
						if _, err := tx.Exec(`INSERT INTO native_cleanup_items(handle,item,state) VALUES(?,?,?)`, string(handle), item, state); err != nil {
							return err
						}
					}
					return nil
				}))
			}
			if backupHasInventory {
				insert(handles[0], "retained")
			}
			artifact, _, err := manager.Create(ctx, "authority", "cleanup-restore")
			require.NoError(t, err)
			if !backupHasInventory {
				insert(handles[0], "retained")
			}
			insert(handles[1], "uncertain")
			require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
				if _, err := tx.Exec(`UPDATE native_cleanup_items SET state='deleted' WHERE handle=?`, string(handles[0])); err != nil {
					return err
				}
				_, err := tx.Exec(`UPDATE native_cleanup_items SET state='reappeared' WHERE handle=? AND item=-1`, string(handles[0]))
				return err
			}))
			root := owner.Layout().Root
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: manager.clock, Entropy: rand.Reader})
			require.NoError(t, err)
			restoredOwner, err := gatewaypaths.AcquireStoppedExisting(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, restoredOwner.Close()) }()
			_, err = storage.InspectMaintenance(ctx, restoredOwner, func(tx *sql.Tx) error {
				var count int
				require.NoError(t, tx.QueryRow(`SELECT count(*) FROM native_cleanup`).Scan(&count))
				require.Equal(t, 2, count)
				for i, h := range handles {
					var state string
					require.NoError(t, tx.QueryRow(`SELECT state FROM native_cleanup_items WHERE handle=? AND item=-1`, string(h)).Scan(&state))
					require.Equal(t, []string{"reappeared", "uncertain"}[i], state)
				}
				var state string
				require.NoError(t, tx.QueryRow(`SELECT state FROM native_cleanup_items WHERE handle=? AND item=0`, string(handles[0])).Scan(&state))
				require.Equal(t, "deleted", state)
				return nil
			})
			require.NoError(t, err)
		})
	}
}
