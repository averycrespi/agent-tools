package keyring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestEncryptedCustodyRefusesLegacyWithoutDiscardingReferences(t *testing.T) {
	owner, store, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	coordinator := NewCoordinator(provider, store, testutil.NewFakeClock(time.Now()), rand.Reader)
	selected, err := coordinator.Replace(t.Context(), ns, []byte("previously migrated material"))
	require.NoError(t, err)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_generations SET custody='legacy',version=NULL,key_id=NULL,ciphertext=NULL WHERE handle=?`, string(selected.Handle))
		return err
	}))
	keyBefore, err := os.ReadFile(filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	require.ErrorIs(t, SetupCustody(t.Context(), owner, store, testutil.NewFakeClock(time.Now())), storage.ErrLegacyCustody)
	restarted, err := NewProvider(testInstallationID)
	require.NoError(t, err)
	require.ErrorIs(t, restarted.UseDatabaseCustody(t.Context(), owner, store), storage.ErrLegacyCustody)
	_, err = provider.ReadGeneration(t.Context(), ns, selected.Handle)
	require.ErrorIs(t, err, storage.ErrLegacyCustody)
	require.ErrorIs(t, provider.DeleteGeneration(t.Context(), ns, selected.Handle), storage.ErrLegacyCustody)
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		var origin, handle string
		require.NoError(t, tx.QueryRow(`SELECT custody FROM secret_generations WHERE handle=?`, string(selected.Handle)).Scan(&origin))
		require.Equal(t, "legacy", origin)
		require.NoError(t, tx.QueryRow(`SELECT handle FROM keyring_authorities WHERE owner=? AND kind=?`, ns.owner, ns.kind).Scan(&handle))
		require.Equal(t, string(selected.Handle), handle)
		return nil
	}))
	keyAfter, err := os.ReadFile(filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	require.Equal(t, keyBefore, keyAfter)
}

func TestEncryptedCustodySetupAdoptsOnlyCompleteInterruptedKey(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "partial"}[complete], func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0o700))
			owner, err := gatewaypaths.AcquireForMaintenance(root)
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			store, err := storage.Initialize(t.Context(), owner, testInstallationID)
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			key := make([]byte, 32)
			_, err = rand.Read(key)
			require.NoError(t, err)
			if !complete {
				key = key[:7]
			}
			path := filepath.Join(root, gatewaypaths.MasterKeyName)
			require.NoError(t, os.WriteFile(path, key, 0o600))
			err = SetupCustody(t.Context(), owner, store, testutil.NewFakeClock(time.Now()))
			if complete {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrCustodyUnavailable)
			}
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, key, actual)
			id, err := custodyKeyID(context.Background(), store)
			require.NoError(t, err)
			if complete {
				require.Equal(t, masterKeyID(key), id)
			} else {
				require.Empty(t, id)
			}
		})
	}
}
