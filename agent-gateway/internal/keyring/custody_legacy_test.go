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

func TestEncryptedCustodyLegacySelectionNeverResurrectsAfterUpdate(t *testing.T) {
	owner, store, _ := custodyFixture(t)
	backend := newMemoryAdapter()
	provider, err := NewProviderWithBackend(testInstallationID, backend)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Now())
	coordinator := NewCoordinator(provider, store, clock, rand.Reader)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	old, err := coordinator.Replace(t.Context(), ns, []byte("legacy"))
	require.NoError(t, err)
	// This is exactly the origin recorded by schema-23 migration for old handles.
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO secret_generations (handle, owner, kind, custody) VALUES (?, ?, ?, 'legacy')`, string(old.Handle), ns.owner, ns.kind)
		return err
	}))
	require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, store))
	secret, _, err := coordinator.ReadActive(t.Context(), ns)
	require.NoError(t, err)
	require.Equal(t, []byte("legacy"), secret)
	retained := backend.values()
	writes, deletes := backend.setCalls, backend.deleteCalls
	current, err := coordinator.Replace(t.Context(), ns, []byte("encrypted"))
	require.NoError(t, err)
	require.Equal(t, writes, backend.setCalls)
	require.Equal(t, deletes, backend.deleteCalls)
	require.Equal(t, retained, backend.values())
	// Even a native entry with the *new* handle cannot authorize fallback.
	native, err := NewProviderWithBackend(testInstallationID, backend)
	require.NoError(t, err)
	require.NoError(t, native.WriteGeneration(t.Context(), ns, current.Handle, []byte("stale-native")))
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM secret_generations WHERE handle = ?`, string(current.Handle))
		return err
	}))
	reads := backend.getCalls
	_, _, err = coordinator.ReadActive(t.Context(), ns)
	require.ErrorIs(t, err, ErrIncompleteGeneration)
	require.Equal(t, reads, backend.getCalls)
	_, err = coordinator.InvalidateFenced(t.Context(), ns, nil)
	require.NoError(t, err)
	_, _, err = coordinator.ReadActive(t.Context(), ns)
	require.ErrorIs(t, err, ErrNoAuthority)
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
