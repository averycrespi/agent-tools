package backup

import (
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEncryptedRestorePreservesCleanupEvidenceAndRefusesUnresolvedState(t *testing.T) {
	for _, state := range []string{"deleted", "retained", "uncertain", "reappeared"} {
		t.Run(state, func(t *testing.T) {
			ctx := audit.WithSystem(t.Context())
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			artifact, _, err := manager.Create(ctx, "authority", "cleanup-restore")
			require.NoError(t, err)
			handle, err := keyring.NewHandle(rand.Reader)
			require.NoError(t, err)
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
			root, database := owner.Layout().Root, owner.Layout().Database
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			before, err := os.ReadFile(database)
			require.NoError(t, err)
			keyBefore, err := os.ReadFile(filepath.Join(root, gatewaypaths.MasterKeyName))
			require.NoError(t, err)
			sink := new(captureSink)
			_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: sink, Clock: manager.clock, Entropy: rand.Reader})
			if state != "deleted" {
				require.ErrorIs(t, err, storage.ErrLegacyCustody)
				require.Empty(t, sink.bearer, "refused restore must not publish replacement credentials")
				after, readErr := os.ReadFile(database)
				require.NoError(t, readErr)
				require.Equal(t, before, after, "refused restore must preserve all references and provenance")
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, sink.bearer)
				restoredOwner, openErr := gatewaypaths.AcquireStoppedExisting(root)
				require.NoError(t, openErr)
				defer func() { require.NoError(t, restoredOwner.Close()) }()
				_, err = storage.InspectMaintenance(ctx, restoredOwner, func(tx *sql.Tx) error {
					var count int
					require.NoError(t, tx.QueryRow(`SELECT count(*) FROM native_cleanup WHERE handle=?`, string(handle)).Scan(&count))
					require.Equal(t, 1, count)
					require.NoError(t, tx.QueryRow(`SELECT count(*) FROM native_cleanup_items WHERE handle=? AND state='deleted'`, string(handle)).Scan(&count))
					require.Equal(t, 118, count, "restore must carry complete newer cleanup evidence forward")
					return nil
				})
				require.NoError(t, err)
			}
			keyAfter, err := os.ReadFile(filepath.Join(root, gatewaypaths.MasterKeyName))
			require.NoError(t, err)
			require.Equal(t, keyBefore, keyAfter)
		})
	}
}
