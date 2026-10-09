package keyring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMasterKeyRotationPreservesAllKindsAndAuthority(t *testing.T) {
	owner, store, provider := custodyFixture(t)
	clock := testutil.NewFakeClock(time.Now())
	coordinator := NewCoordinator(provider, store, clock, rand.Reader)
	ctx := WithGitAuthorityAdmission(t.Context(), func(context.Context) (func(), error) { return func() {}, nil })
	kinds := []RecordKind{RecordStaticCredential, RecordOAuthClient, RecordOAuthTokens, RecordHTTPCredential, RecordGitCredential, RecordHTTPCA}
	handles := make(map[RecordKind]Handle)
	for _, kind := range kinds {
		ns, err := NewNamespace(testInstallationID, testOwnerID, kind)
		require.NoError(t, err)
		handle, err := coordinator.Replace(ctx, ns, []byte("rotation-canary-"+kind))
		require.NoError(t, err)
		handles[kind] = handle.Handle
	}
	oldKey, err := gatewaypaths.MasterKey(owner, nil)
	require.NoError(t, err)
	defer clear(oldKey)
	before := map[string][]byte{}
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT handle,ciphertext FROM secret_generations`)
		if err != nil {
			return err
		}
		defer func() { require.NoError(t, rows.Close()) }()
		for rows.Next() {
			var h string
			var v []byte
			require.NoError(t, rows.Scan(&h, &v))
			before[h] = v
		}
		return rows.Err()
	}))
	require.NoError(t, store.Close())
	result, err := RotateStoppedMasterKey(ctx, owner, clock, false, nil)
	require.NoError(t, err)
	require.Equal(t, "rotated", result.Disposition)
	retained, err := gatewaypaths.ReadRecoveryKey(filepath.Join(owner.Layout().Root, result.Retained, "old-key"))
	require.NoError(t, err)
	require.Equal(t, oldKey, retained)
	clear(retained)
	newKey, err := gatewaypaths.MasterKey(owner, nil)
	require.NoError(t, err)
	defer clear(newKey)
	require.NotEqual(t, oldKey, newKey)
	reopened, err := storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	restarted, err := NewProvider(testInstallationID)
	require.NoError(t, err)
	require.NoError(t, restarted.UseDatabaseCustody(ctx, owner, reopened))
	reader := NewCoordinator(restarted, reopened, clock, rand.Reader)
	for _, kind := range kinds {
		ns, err := NewNamespace(testInstallationID, testOwnerID, kind)
		require.NoError(t, err)
		value, handle, err := reader.ReadActive(ctx, ns)
		require.NoError(t, err)
		require.Equal(t, []byte("rotation-canary-"+kind), value)
		require.Equal(t, handles[kind], handle.Handle)
	}
	require.NoError(t, reopened.View(ctx, func(tx *sql.Tx) error {
		verified, err := VerifyBackupCustodyTx(ctx, tx, owner, nil)
		require.NoError(t, err)
		require.EqualValues(t, len(kinds), verified.Encryptions)
		_, err = VerifyRecoveryCustodyTx(ctx, tx, oldKey, nil)
		require.ErrorIs(t, err, ErrCustodyUnavailable)
		for h, old := range before {
			var current []byte
			require.NoError(t, tx.QueryRow(`SELECT ciphertext FROM secret_generations WHERE handle=?`, h).Scan(&current))
			require.NotEqual(t, old, current)
		}
		return nil
	}))
}

func TestMasterKeyRotationFaultsReconcileWithoutReplay(t *testing.T) {
	for _, point := range []string{"armed", "before_database_commit", "database_commit", "database_closed", "key_published", "retained"} {
		t.Run(point, func(t *testing.T) {
			owner, store, provider := custodyFixture(t)
			ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			handle, err := NewHandle(rand.Reader)
			require.NoError(t, err)
			require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, []byte("preserved")))
			old, err := gatewaypaths.MasterKey(owner, nil)
			require.NoError(t, err)
			defer clear(old)
			require.NoError(t, store.Close())
			clock := testutil.NewFakeClock(time.Now())
			injected := errors.New("stop")
			_, err = rotateStoppedMasterKey(t.Context(), owner, clock, false, nil, func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			if point != "retained" {
				_, err = storage.Open(t.Context(), owner)
				require.ErrorIs(t, err, gatewaypaths.ErrKeyRotationPending)
				_, err = RotateStoppedMasterKey(t.Context(), owner, clock, false, nil)
				require.ErrorIs(t, err, gatewaypaths.ErrKeyRotationPending)
				result, err := RotateStoppedMasterKey(t.Context(), owner, clock, true, nil)
				require.NoError(t, err)
				if point == "armed" || point == "before_database_commit" {
					require.Equal(t, "aborted-before-commit", result.Disposition)
				} else {
					require.Equal(t, "completed-committed-rotation", result.Disposition)
				}
			}
			reopened, err := storage.Open(t.Context(), owner)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, reopened))
			value, err := provider.ReadGeneration(t.Context(), ns, handle)
			require.NoError(t, err)
			require.Equal(t, []byte("preserved"), value)
			var used int64
			require.NoError(t, reopened.View(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&used) }))
			require.EqualValues(t, 1, used)
			current, err := gatewaypaths.MasterKey(owner, nil)
			require.NoError(t, err)
			defer clear(current)
			if point == "armed" || point == "before_database_commit" {
				require.Equal(t, old, current)
			} else {
				require.NotEqual(t, old, current)
			}
		})
	}
}

func TestMasterKeyRotationRefusesLegacyWrongKeyAndConsent(t *testing.T) {
	for _, mode := range []string{"legacy", "wrong-key", "consent"} {
		t.Run(mode, func(t *testing.T) {
			owner, store, _ := custodyFixture(t)
			if mode == "legacy" {
				require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) VALUES ('legacy',?,?,'legacy')`, testOwnerID, RecordStaticCredential)
					return err
				}))
			}
			if mode == "wrong-key" {
				require.NoError(t, os.WriteFile(filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName), make([]byte, 32), 0600))
			}
			require.NoError(t, store.Close())
			denied := errors.New("not approved")
			_, err := RotateStoppedMasterKey(t.Context(), owner, testutil.NewFakeClock(time.Now()), false, func(context.Context, *gatewaypaths.Ownership) error { return denied })
			require.Error(t, err)
			if mode == "consent" {
				require.ErrorIs(t, err, denied)
			}
			require.NoError(t, gatewaypaths.RequireNoKeyRotation(owner))
		})
	}
}
