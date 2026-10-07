package keyring

import (
	"bytes"
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

type forbiddenNative struct{ t *testing.T }

func (b forbiddenNative) Probe(context.Context, string) error { b.t.Fatal("native probe"); return nil }
func (b forbiddenNative) Get(string, string) (string, error) {
	b.t.Fatal("native read")
	return "", nil
}
func (b forbiddenNative) Set(string, string, string) error { b.t.Fatal("native write"); return nil }
func (b forbiddenNative) Delete(string, string) error      { b.t.Fatal("native delete"); return nil }

func custodyFixture(t *testing.T) (*gatewaypaths.Ownership, *storage.Store, *Provider) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	owner, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	store, err := storage.Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()); require.NoError(t, owner.Close()) })
	require.NoError(t, SetupCustody(t.Context(), owner, store, testutil.NewFakeClock(time.Now())))
	provider, err := NewProviderWithBackend(testInstallationID, forbiddenNative{t})
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(t.Context(), owner, store))
	return owner, store, provider
}

func TestEncryptedCustodyAllKindsPersistWithoutPlaintextOrNativeAccess(t *testing.T) {
	owner, store, provider := custodyFixture(t)
	clock := testutil.NewFakeClock(time.Now())
	coordinator := NewCoordinator(provider, store, clock, rand.Reader)
	secret := []byte("unique-plaintext-canary-not-for-durable-storage")
	ctx := WithGitAuthorityAdmission(t.Context(), func(context.Context) (func(), error) { return func() {}, nil })
	for _, kind := range []RecordKind{RecordStaticCredential, RecordOAuthClient, RecordOAuthTokens, RecordHTTPCredential, RecordGitCredential, RecordHTTPCA} {
		ns, err := NewNamespace(testInstallationID, testOwnerID, kind)
		require.NoError(t, err)
		_, err = coordinator.Replace(ctx, ns, secret)
		require.NoError(t, err)
		actual, _, err := coordinator.ReadActive(t.Context(), ns)
		require.NoError(t, err)
		require.Equal(t, secret, actual)
	}
	// Reconstruct the provider/key schedule as startup does; no native read or probe.
	restarted, err := NewProviderWithBackend(testInstallationID, forbiddenNative{t})
	require.NoError(t, err)
	require.NoError(t, restarted.UseDatabaseCustody(t.Context(), owner, store))
	require.Equal(t, provider.Probe(t.Context()), restarted.Probe(t.Context()))
	reader := NewCoordinator(restarted, store, clock, rand.Reader)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordHTTPCA)
	require.NoError(t, err)
	actual, _, err := reader.ReadActive(t.Context(), ns)
	require.NoError(t, err)
	require.Equal(t, secret, actual)
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(owner.Layout().Database + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		require.NoError(t, err)
		require.False(t, bytes.Contains(data, secret), "plaintext in database%s", suffix)
	}
}

func TestEncryptedCustodyAuthenticatesBindingAndFreshNonces(t *testing.T) {
	_, store, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	h1, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	h2, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, provider.WriteGeneration(t.Context(), ns, h1, []byte("same secret")))
	require.NoError(t, provider.WriteGeneration(t.Context(), ns, h2, []byte("same secret")))
	var a, b []byte
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		require.NoError(t, tx.QueryRow(`SELECT ciphertext FROM secret_generations WHERE handle = ?`, string(h1)).Scan(&a))
		return tx.QueryRow(`SELECT ciphertext FROM secret_generations WHERE handle = ?`, string(h2)).Scan(&b)
	}))
	require.NotEqual(t, a, b)
	for _, name := range []string{"nonce", "ciphertext", "tag", "substitution"} {
		t.Run(name, func(t *testing.T) {
			bad := append([]byte(nil), a...)
			switch name {
			case "nonce":
				bad[0] ^= 1
			case "ciphertext":
				bad[12] ^= 1
			case "tag":
				bad[len(bad)-1] ^= 1
			case "substitution":
				bad = b
			}
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE secret_generations SET ciphertext = ? WHERE handle = ?`, bad, string(h1))
				return err
			}))
			_, err := provider.ReadGeneration(t.Context(), ns, h1)
			require.ErrorIs(t, err, ErrIncompleteGeneration)
		})
	}
}

func TestEncryptedCustodyRejectsOwnerAndKindSubstitution(t *testing.T) {
	_, store, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	for _, target := range []Namespace{
		{installationID: testInstallationID, owner: testInstallationID, kind: ns.kind},
		{installationID: testInstallationID, owner: ns.owner, kind: RecordOAuthClient},
	} {
		handle, err := NewHandle(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, []byte("bound material")))
		require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE secret_generations SET owner = ?, kind = ? WHERE handle = ?`, target.owner, target.kind, string(handle))
			return err
		}))
		_, err = provider.ReadGeneration(t.Context(), target, handle)
		require.ErrorIs(t, err, ErrIncompleteGeneration)
	}
}

func TestEncryptedCustodyKeyLossNeverReplacesKey(t *testing.T) {
	for _, fault := range []string{"missing", "wrong", "short", "mode", "link", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			owner, store, _ := custodyFixture(t)
			path := filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName)
			switch fault {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "wrong":
				require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0o600))
			case "short":
				require.NoError(t, os.WriteFile(path, []byte{1}, 0o600))
			case "mode":
				require.NoError(t, os.Chmod(path, 0o644))
			case "link":
				require.NoError(t, os.Link(path, path+"-other"))
			case "symlink":
				require.NoError(t, os.Rename(path, path+"-other"))
				require.NoError(t, os.Symlink(path+"-other", path))
			}
			before, _ := os.ReadFile(path)
			require.ErrorIs(t, SetupCustody(t.Context(), owner, store, testutil.NewFakeClock(time.Now())), ErrCustodyUnavailable)
			provider, err := NewProviderWithBackend(testInstallationID, forbiddenNative{t})
			require.NoError(t, err)
			require.ErrorIs(t, provider.UseDatabaseCustody(t.Context(), owner, store), ErrCustodyUnavailable)
			after, err := os.ReadFile(path)
			if fault == "missing" {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
		})
	}
}

func TestEncryptedCustodyMissingIdentityCannotReinitialize(t *testing.T) {
	owner, store, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	handle, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, []byte("retained ciphertext")))
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec(`DELETE FROM secret_custody`); return err }))
	path := filepath.Join(owner.Layout().Root, gatewaypaths.MasterKeyName)
	require.NoError(t, os.Remove(path))
	require.ErrorIs(t, SetupCustody(t.Context(), owner, store, testutil.NewFakeClock(time.Now())), ErrCustodyUnavailable)
	require.ErrorIs(t, provider.UseDatabaseCustody(t.Context(), owner, store), ErrCustodyUnavailable)
	_, err = os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEncryptedCustodyEnforcesLifetimeNonceBudget(t *testing.T) {
	_, store, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE secret_custody SET encryptions = 4294967295`)
		return err
	}))
	handle, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, []byte("last permitted generation")))
	require.NoError(t, provider.DeleteGeneration(t.Context(), ns, handle))
	next, err := NewHandle(rand.Reader)
	require.NoError(t, err)
	require.ErrorIs(t, provider.WriteGeneration(t.Context(), ns, next, []byte("must refuse")), ErrCustodyUnavailable)
	var count int64
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { return tx.QueryRow(`SELECT encryptions FROM secret_custody`).Scan(&count) }))
	require.EqualValues(t, 4294967296, count)
	_, err = provider.ReadGeneration(t.Context(), ns, next)
	require.ErrorIs(t, err, ErrIncompleteGeneration)
}

func TestEncryptedCustodyFailedPublicationPreservesOnlyPriorSelection(t *testing.T) {
	_, store, provider := custodyFixture(t)
	clock := testutil.NewFakeClock(time.Now())
	coordinator := NewCoordinator(provider, store, clock, rand.Reader)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	prior, err := coordinator.Replace(t.Context(), ns, []byte("old"))
	require.NoError(t, err)
	coordinator.hooks.beforeCommit = injectedCrash
	_, err = coordinator.Replace(t.Context(), ns, []byte("new"))
	require.ErrorIs(t, err, errInjectedCrash)
	value, selection, err := coordinator.ReadActive(t.Context(), ns)
	require.NoError(t, err)
	require.Equal(t, []byte("old"), value)
	require.Equal(t, prior, selection)
	coordinator.hooks.beforeCommit = nil
	_, err = coordinator.InvalidateFenced(t.Context(), ns, nil)
	require.NoError(t, err)
	_, _, err = coordinator.ReadActive(t.Context(), ns)
	require.ErrorIs(t, err, ErrNoAuthority)
}
