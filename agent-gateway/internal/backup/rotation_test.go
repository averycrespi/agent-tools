package backup

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCrossKeyRestoreReservesBudgetBeforeExposure(t *testing.T) {
	t.Parallel()
	for _, point := range []restoreFaultPoint{restoreFaultAfterReservation, restoreFaultBeforeInstall, ""} {
		t.Run(string(point), func(t *testing.T) {
			ctx := t.Context()
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			provider, err := keyring.NewProvider(backupTestInstallationID)
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
			ns, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, keyring.RecordStaticCredential)
			require.NoError(t, err)
			handle, err := keyring.NewHandle(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, provider.WriteGeneration(ctx, ns, handle, []byte("historical-secret")))
			artifact, _, err := manager.Create(ctx, "authority", "before-rotation")
			require.NoError(t, err)
			root := owner.Layout().Root
			backupPath := filepath.Join(owner.Layout().Backups, artifact.ID, databaseFile)
			before, err := digestFile(backupPath)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			rotation, err := keyring.RotateStoppedMasterKey(ctx, owner, manager.clock, false, nil)
			require.NoError(t, err)
			oldPath := filepath.Join(root, rotation.Retained, "old-key")
			active, err := gatewaypaths.MasterKey(owner, nil)
			require.NoError(t, err)
			defer clear(active)
			require.NoError(t, owner.Close())
			injected := errors.New("interrupt")
			options := RestoreOptions{Root: root, BackupID: artifact.ID, RecoveryKey: oldPath, Sink: new(captureSink), Clock: manager.clock, Entropy: rand.Reader, fault: func(actual restoreFaultPoint) error {
				if actual == point {
					return injected
				}
				return nil
			}}
			_, err = Restore(ctx, options)
			if point != "" {
				require.ErrorIs(t, err, injected)
			} else {
				require.NoError(t, err)
			}
			inspect := func(want int64) {
				stopped, err := gatewaypaths.AcquireStoppedExisting(root)
				require.NoError(t, err)
				defer func() { require.NoError(t, stopped.Close()) }()
				_, err = storage.InspectMaintenance(ctx, stopped, func(tx *sql.Tx) error {
					v, err := keyring.RestoreHighWaterTx(ctx, tx)
					require.NoError(t, err)
					require.Equal(t, want, v.Encryptions)
					return nil
				})
				require.NoError(t, err)
				now, err := gatewaypaths.MasterKey(stopped, nil)
				require.NoError(t, err)
				require.Equal(t, active, now)
				clear(now)
			}
			inspect(2)
			options.fault = nil
			options.Sink = new(captureSink)
			_, err = Restore(ctx, options)
			require.NoError(t, err)
			inspect(3)
			after, err := digestFile(backupPath)
			require.NoError(t, err)
			require.Equal(t, before, after, "backup is never rewritten")
			stopped, err := gatewaypaths.AcquireStoppedExisting(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, stopped.Close()) }()
			restored, err := storage.Open(ctx, stopped)
			require.NoError(t, err)
			defer func() { require.NoError(t, restored.Close()) }()
			require.NoError(t, provider.UseDatabaseCustody(ctx, stopped, restored))
			secret, err := provider.ReadGeneration(ctx, ns, handle)
			require.NoError(t, err)
			require.Equal(t, []byte("historical-secret"), secret)
			old, err := gatewaypaths.ReadRecoveryKey(oldPath)
			require.NoError(t, err)
			defer clear(old)
			require.NoError(t, restored.View(ctx, func(tx *sql.Tx) error {
				_, err := keyring.VerifyRecoveryCustodyTx(ctx, tx, old, nil)
				require.ErrorIs(t, err, keyring.ErrCustodyUnavailable)
				return nil
			}))
		})
	}
}

func TestCrossKeyRestoreRefusesMissingWrongAndExhaustedKeys(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "wrong", "exhausted", "pending-rotation"} {
		t.Run(mode, func(t *testing.T) {
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(t.Context(), new(captureSink))
			require.NoError(t, err)
			provider, err := keyring.NewProvider(backupTestInstallationID)
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, store))
			ns, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, keyring.RecordStaticCredential)
			require.NoError(t, err)
			handle, err := keyring.NewHandle(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, []byte("secret")))
			artifact, _, err := manager.Create(t.Context(), "authority", "old")
			require.NoError(t, err)
			require.NoError(t, store.Close())
			rotation, err := keyring.RotateStoppedMasterKey(t.Context(), owner, manager.clock, false, nil)
			require.NoError(t, err)
			path := filepath.Join(owner.Layout().Root, rotation.Retained, "old-key")
			switch mode {
			case "missing":
				path = ""
			case "wrong":
				path = filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName)
			case "exhausted":
				s, err := storage.Open(t.Context(), owner)
				require.NoError(t, err)
				require.NoError(t, s.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE secret_custody SET encryptions=4294967296`)
					return err
				}))
				require.NoError(t, s.Close())
			case "pending-rotation":
				require.NoError(t, os.Mkdir(filepath.Join(owner.Layout().Root, gatewaypaths.KeyRotationName), 0700))
			}
			require.NoError(t, owner.Close())
			before, err := digestFile(owner.Layout().Database)
			require.NoError(t, err)
			sink := new(captureSink)
			_, err = Restore(t.Context(), RestoreOptions{Root: owner.Layout().Root, BackupID: artifact.ID, RecoveryKey: path, Sink: sink, Clock: manager.clock, Entropy: rand.Reader})
			require.Error(t, err)
			require.Empty(t, sink.bearer)
			after, err := digestFile(owner.Layout().Database)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
