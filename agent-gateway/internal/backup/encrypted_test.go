package backup

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEncryptedRestoreRetainsMaterialAndBudgetResetsAccess(t *testing.T) {
	for _, point := range []restoreFaultPoint{"", restoreFaultBeforeInstall, restoreFaultAfterInstall} {
		t.Run(string(point), func(t *testing.T) {
			ctx := t.Context()
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
			provider, err := keyring.NewProvider(backupTestInstallationID)
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
			oldAdmin := new(captureSink)
			_, err = admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, oldAdmin)
			require.NoError(t, err)
			authority, err := authorization.New(store, manager.clock, rand.Reader)
			require.NoError(t, err)
			principal, err := authority.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "recover", Visibility: contract.VisibilityAll})
			require.NoError(t, err)
			oldAgent, err := authority.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
			require.NoError(t, err)
			kinds := []keyring.RecordKind{keyring.RecordStaticCredential, keyring.RecordOAuthClient, keyring.RecordOAuthTokens, keyring.RecordHTTPCredential, keyring.RecordGitCredential, keyring.RecordHTTPCA}
			handles := make([]keyring.Handle, len(kinds))
			for i, kind := range kinds {
				ns, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, kind)
				require.NoError(t, err)
				handles[i], err = keyring.NewHandle(rand.Reader)
				require.NoError(t, err)
				require.NoError(t, provider.WriteGeneration(ctx, ns, handles[i], []byte("recovery-canary-"+string(kind))))
			}
			artifact, _, err := manager.Create(ctx, "authority", "encrypted")
			require.NoError(t, err)
			directory := filepath.Join(owner.Layout().Backups, artifact.ID)
			raw, err := os.ReadFile(filepath.Join(directory, metadataFile))
			require.NoError(t, err)
			var metadata artifactMetadata
			require.NoError(t, json.Unmarshal(raw, &metadata))
			require.Equal(t, 4, metadata.Format)
			require.Len(t, metadata.MasterKeyID, 64)
			key, err := gatewaypaths.MasterKey(owner, nil)
			require.NoError(t, err)
			defer clear(key)
			for _, name := range []string{databaseFile, metadataFile} {
				content, err := os.ReadFile(filepath.Join(directory, name))
				require.NoError(t, err)
				require.NotContains(t, string(content), string(key))
				require.NotContains(t, string(content), "recovery-canary-")
				require.NotContains(t, string(content), oldAdmin.bearer)
				require.NotContains(t, string(content), oldAgent.Bearer)
			}
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Len(t, entries, 2)
			// A later publication must not be refunded, even when restoring repeatedly.
			ns, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, kinds[0])
			require.NoError(t, err)
			later, err := keyring.NewHandle(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, provider.WriteGeneration(ctx, ns, later, []byte("later")))
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			fresh := new(captureSink)
			injected := errors.New("interrupt")
			_, err = Restore(ctx, RestoreOptions{Root: owner.Layout().Root, BackupID: artifact.ID, Sink: fresh, Clock: manager.clock, Entropy: rand.Reader, fault: func(actual restoreFaultPoint) error {
				if point != "" && point == actual {
					return injected
				}
				return nil
			}})
			if point == "" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, injected)
			}
			reopened, err := gatewaypaths.AcquireForMaintenance(owner.Layout().Root)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			restored, err := storage.OpenReplacement(ctx, reopened, reopened.Layout().Database)
			require.NoError(t, err)
			defer func() { require.NoError(t, restored.Close()) }()
			require.NoError(t, provider.UseDatabaseCustody(ctx, reopened, restored))
			var count int
			require.NoError(t, restored.View(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&count) }))
			require.Equal(t, 7, count)
			for i, kind := range kinds {
				ns, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, kind)
				require.NoError(t, err)
				material, err := provider.ReadGeneration(ctx, ns, handles[i])
				require.NoError(t, err)
				require.Equal(t, "recovery-canary-"+string(kind), string(material))
				clear(material)
			}
			admins := admin.NewService(restored, manager.clock, rand.Reader)
			_, oldErr := admins.Authenticate(ctx, oldAdmin.bearer)
			auth, err := authorization.New(restored, manager.clock, rand.Reader)
			require.NoError(t, err)
			_, agentErr := auth.Authenticate(ctx, oldAgent.Bearer)
			if point == restoreFaultBeforeInstall {
				require.NoError(t, oldErr)
				require.NoError(t, agentErr)
			} else {
				require.Error(t, oldErr)
				require.Error(t, agentErr)
				_, err = admins.Authenticate(ctx, fresh.bearer)
				require.NoError(t, err)
			}
			if point == "" {
				require.NoError(t, restored.Close())
				require.NoError(t, reopened.Close())
				again := new(captureSink)
				_, err = Restore(ctx, RestoreOptions{Root: owner.Layout().Root, BackupID: artifact.ID, Sink: again, Clock: manager.clock, Entropy: rand.Reader})
				require.NoError(t, err)
				require.NoError(t, storage.ViewBackup(ctx, owner.Layout().Database, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&count) }))
				require.Equal(t, 7, count)
			}
		})
	}
}

func TestEncryptedRestoreRefusesWrongKeyAndCorruptionBeforeStage(t *testing.T) {
	for _, bad := range []string{"missing-key", "wrong-key", "corrupt-artifact", "legacy-dependency"} {
		t.Run(bad, func(t *testing.T) {
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(t.Context(), owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(t.Context(), new(captureSink))
			require.NoError(t, err)
			artifact, _, err := manager.Create(t.Context(), "authority", "encrypted")
			require.NoError(t, err)
			root := owner.Layout().Root
			if bad == "legacy-dependency" {
				handle, err := keyring.NewHandle(rand.Reader)
				require.NoError(t, err)
				require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) VALUES(?,?,'static_credential','legacy')`, handle, backupTestInstallationID)
					return err
				}))
				_, _, err = manager.Create(t.Context(), "authority", "mixed")
				require.ErrorIs(t, err, ErrEncryptedCustodyUnsupported)
				return
			}
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			before, err := os.ReadFile(filepath.Join(root, databaseFile))
			require.NoError(t, err)
			switch bad {
			case "missing-key":
				require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.MasterKeyName)))
			case "wrong-key":
				require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.MasterKeyName), make([]byte, 32), 0600))
			case "corrupt-artifact":
				require.NoError(t, os.WriteFile(filepath.Join(root, "backups", artifact.ID, databaseFile), []byte("corrupt"), 0600))
			}
			sink := new(captureSink)
			_, err = Restore(t.Context(), RestoreOptions{Root: root, BackupID: artifact.ID, Sink: sink, Clock: manager.clock, Entropy: rand.Reader})
			require.Error(t, err)
			require.Empty(t, sink.bearer)
			after, err := os.ReadFile(filepath.Join(root, databaseFile))
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = os.Lstat(filepath.Join(root, databaseFile+".restore"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
