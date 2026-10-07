package keyring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func allowMigrationMaterial(context.Context, *sql.Tx, RecordKind, Handle, []byte) error { return nil }
func allowCleanup(context.Context, *sql.Tx) error                                       { return nil }

func legacyMigrationRecord(t *testing.T, store *storage.Store, native *Provider, kind RecordKind) (Namespace, Handle) {
	t.Helper()
	ns, err := NewNamespace(testInstallationID, testOwnerID, kind)
	require.NoError(t, err)
	handle, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, native.WriteGeneration(t.Context(), ns, handle, []byte("migration-private-"+string(kind))))
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO keyring_authorities(owner,kind,handle,revision) VALUES(?,?,?,1)`, ns.owner, kind, string(handle)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) VALUES(?,?,?,'legacy')`, string(handle), ns.owner, kind)
		return err
	}))
	return ns, handle
}

func TestSecretMigrationAllKindsRetainsSourcesAndNeverFallsBack(t *testing.T) {
	t.Parallel()
	owner, store, encrypted := custodyFixture(t)
	backend := newMemoryAdapter()
	native, err := NewProviderWithBackend(testInstallationID, backend)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Now())
	type record struct {
		ns     Namespace
		handle Handle
	}
	var records []record
	for _, kind := range []RecordKind{RecordStaticCredential, RecordOAuthClient, RecordOAuthTokens, RecordHTTPCredential, RecordGitCredential, RecordHTTPCA} {
		ns, h := legacyMigrationRecord(t, store, native, kind)
		records = append(records, record{ns, h})
	}
	before := backend.values()
	writes, deletes := backend.setCalls, backend.deleteCalls
	result, err := MigrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial)
	require.NoError(t, err)
	require.Equal(t, 6, result.Migrated)
	require.Equal(t, before, backend.values())
	require.Equal(t, writes, backend.setCalls)
	require.Equal(t, deletes, backend.deleteCalls)
	for _, r := range records {
		payload, err := encrypted.ReadGeneration(t.Context(), r.ns, r.handle)
		require.NoError(t, err)
		require.Equal(t, "migration-private-"+string(r.ns.kind), string(payload))
	}
	reads := backend.getCalls
	result, err = MigrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial)
	require.NoError(t, err)
	require.Zero(t, result.Migrated)
	require.Equal(t, reads, backend.getCalls)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_generations SET ciphertext=zeroblob(length(ciphertext)) WHERE handle=?`, string(records[0].handle))
		return err
	}))
	_, err = encrypted.ReadGeneration(t.Context(), records[0].ns, records[0].handle)
	require.Error(t, err)
	require.Equal(t, reads, backend.getCalls)
}

func TestSecretMigrationFaultsPreserveReservationAndCurrentAuthority(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"reserved", "copied", "before_commit", "committed"} {
		t.Run(point, func(t *testing.T) {
			owner, store, encrypted := custodyFixture(t)
			backend := newMemoryAdapter()
			native, err := NewProviderWithBackend(testInstallationID, backend)
			require.NoError(t, err)
			ns, handle := legacyMigrationRecord(t, store, native, RecordOAuthTokens)
			clock := testutil.NewFakeClock(time.Now())
			injected := errors.New("injected")
			failed, err := migrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial, func(at string) error {
				if at == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			require.True(t, failed.RemainingKnown)
			if point == "committed" {
				require.Zero(t, failed.Remaining)
			} else {
				require.Equal(t, 1, failed.Remaining)
			}
			var count int
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&count) }))
			require.Equal(t, 1, count)
			result, err := MigrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial)
			require.NoError(t, err)
			expected := 1
			if point == "committed" {
				expected = 0
			}
			require.Equal(t, expected, result.Migrated)
			payload, err := encrypted.ReadGeneration(t.Context(), ns, handle)
			require.NoError(t, err)
			require.Equal(t, "migration-private-oauth_tokens", string(payload))
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&count) }))
			require.Equal(t, 1+expected, count)
		})
	}
}

func TestSecretMigrationRejectsChangedDeletedAndFencedAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"changed", "deleted", "fenced"} {
		t.Run(change, func(t *testing.T) {
			owner, store, _ := custodyFixture(t)
			backend := newMemoryAdapter()
			native, err := NewProviderWithBackend(testInstallationID, backend)
			require.NoError(t, err)
			ns, h := legacyMigrationRecord(t, store, native, RecordStaticCredential)
			_, err = migrateLegacy(t.Context(), owner, store, native, testutil.NewFakeClock(time.Now()), allowMigrationMaterial, func(at string) error {
				if at != "copied" {
					return nil
				}
				return store.Mutate(t.Context(), func(tx *sql.Tx) error {
					query := `UPDATE keyring_authorities SET revision=revision+1 WHERE handle=?`
					if change == "deleted" {
						query = `DELETE FROM keyring_authorities WHERE handle=?`
					}
					if change == "fenced" {
						_, err := tx.Exec(`INSERT INTO keyring_authority_fences(owner,kind) VALUES(?,?)`, ns.owner, ns.kind)
						return err
					}
					_, err := tx.Exec(query, string(h))
					return err
				})
			})
			require.ErrorIs(t, err, ErrMigrationIncomplete)
			var origin string
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT custody FROM secret_generations WHERE handle=?`, string(h)).Scan(&origin)
			}))
			require.Equal(t, "legacy", origin)
		})
	}
}

func TestSecretMigrationPartialMissingSourceResumes(t *testing.T) {
	t.Parallel()
	owner, store, _ := custodyFixture(t)
	backend := newMemoryAdapter()
	native, err := NewProviderWithBackend(testInstallationID, backend)
	require.NoError(t, err)
	ns, h := legacyMigrationRecord(t, store, native, RecordStaticCredential)
	legacyMigrationRecord(t, store, native, RecordOAuthTokens)
	manifest := backend.values()[generationManifestItem(ns, h)]
	require.NoError(t, backend.Delete("", generationManifestItem(ns, h)))
	clock := testutil.NewFakeClock(time.Now())
	result, err := MigrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial)
	require.ErrorIs(t, err, ErrMigrationIncomplete)
	require.Equal(t, 1, result.Migrated)
	require.Equal(t, 1, result.Remaining)
	backend.put(generationManifestItem(ns, h), manifest)
	result, err = MigrateLegacy(t.Context(), owner, store, native, clock, allowMigrationMaterial)
	require.NoError(t, err)
	require.Equal(t, 1, result.Migrated)
}

func TestSecretMigrationFailureReconcilesUnvisitedOrUnknownRemaining(t *testing.T) {
	t.Parallel()
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			owner, store, _ := custodyFixture(t)
			native, err := NewProviderWithBackend(testInstallationID, newMemoryAdapter())
			require.NoError(t, err)
			legacyMigrationRecord(t, store, native, RecordStaticCredential)
			legacyMigrationRecord(t, store, native, RecordOAuthTokens)
			rejected := errors.New("invalid domain payload")
			inspect := func(context.Context, *sql.Tx, RecordKind, Handle, []byte) error { return rejected }
			var fault func(string) error
			if unavailable {
				inspect = allowMigrationMaterial
				fault = func(at string) error {
					if at == "copied" {
						require.NoError(t, store.Close())
						return rejected
					}
					return nil
				}
			}
			result, err := migrateLegacy(t.Context(), owner, store, native, testutil.NewFakeClock(time.Now()), inspect, fault)
			require.ErrorIs(t, err, rejected)
			require.Equal(t, !unavailable, result.RemainingKnown)
			if !unavailable {
				require.Equal(t, 2, result.Remaining)
			}
		})
	}
}

type cleanupFaultBackend struct {
	*memoryAdapter
	uncertain    bool
	deleteEffect bool
}

func (b *cleanupFaultBackend) Delete(service, item string) error {
	if b.uncertain {
		if b.deleteEffect {
			_ = b.memoryAdapter.Delete(service, item)
		}
		return errors.New("private native failure")
	}
	return b.memoryAdapter.Delete(service, item)
}

func TestNativeCleanupReconcilesUncertainEffectsAndPreservesUnrelatedItems(t *testing.T) {
	t.Parallel()
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			owner, store, _ := custodyFixture(t)
			backend := &cleanupFaultBackend{memoryAdapter: newMemoryAdapter()}
			native, err := NewProviderWithBackend(testInstallationID, backend)
			require.NoError(t, err)
			ns, h := legacyMigrationRecord(t, store, native, RecordHTTPCredential)
			backend.put("unrelated", "never-delete")
			_, err = MigrateLegacy(t.Context(), owner, store, native, testutil.NewFakeClock(time.Now()), allowMigrationMaterial)
			require.NoError(t, err)
			backend.uncertain = true
			backend.deleteEffect = effect
			result, err := CleanupNative(t.Context(), store, native, allowCleanup)
			if effect {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrMigrationIncomplete)
				require.Positive(t, result.Uncertain)
			}
			backend.uncertain = false
			_, err = CleanupNative(t.Context(), store, native, allowCleanup)
			require.NoError(t, err)
			require.Equal(t, map[string]string{"unrelated": "never-delete"}, backend.values())
			calls := backend.deleteCalls
			_, err = CleanupNative(t.Context(), store, native, allowCleanup)
			require.NoError(t, err)
			require.Equal(t, calls, backend.deleteCalls)
			backend.put(generationManifestItem(ns, h), "reappeared")
			result, err = CleanupNative(t.Context(), store, native, allowCleanup)
			require.ErrorIs(t, err, ErrMigrationIncomplete)
			require.Positive(t, result.Uncertain)
			require.Equal(t, calls, backend.deleteCalls)
		})
	}
}

func TestNativeCleanupRefusesUnverifiedOrForeignOwnership(t *testing.T) {
	t.Parallel()
	owner, store, _ := custodyFixture(t)
	backend := newMemoryAdapter()
	native, err := NewProviderWithBackend(testInstallationID, backend)
	require.NoError(t, err)
	legacyMigrationRecord(t, store, native, RecordHTTPCA)
	_, err = MigrateLegacy(t.Context(), owner, store, native, testutil.NewFakeClock(time.Now()), allowMigrationMaterial)
	require.NoError(t, err)
	_, err = CleanupNative(t.Context(), store, native, func(context.Context, *sql.Tx) error { return ErrMigrationIncomplete })
	require.Error(t, err)
	require.Zero(t, backend.deleteCalls)
	foreign, err := NewProviderWithBackend(testOwnerID, backend)
	require.NoError(t, err)
	_, err = CleanupNative(t.Context(), store, foreign, allowCleanup)
	require.Error(t, err)
	require.Zero(t, backend.deleteCalls)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_generations SET custody='legacy',version=NULL,key_id=NULL,ciphertext=NULL`)
		return err
	}))
	_, err = CleanupNative(t.Context(), store, native, allowCleanup)
	require.Error(t, err)
	require.Zero(t, backend.deleteCalls)
}
