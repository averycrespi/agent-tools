package keyring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithOperationHoldsGlobalAdmissionAcrossCallerWork(t *testing.T) {
	store, _ := newCutoverStore(t)
	provider, err := NewProviderForTest(testInstallationID, NewMemoryAdapterForTest())
	require.NoError(t, err)
	coordinator := NewCoordinator(provider, store, testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)), testutil.NewFakeEntropy(uniqueEntropy(1)))
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- coordinator.WithOperation(context.Background(), func(*Operation) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	_, _, err = coordinator.ReadActive(context.Background(), namespace)
	assert.ErrorIs(t, err, ErrWorkLimit)
	close(release)
	require.NoError(t, <-done)
}

func TestFencedPublicationCommitsGenerationAndDomainMetadataAtomically(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	coordinator := NewCoordinator(
		provider,
		store,
		testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)),
		testutil.NewFakeEntropy(uniqueEntropy(1)),
	)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))

	result, err := coordinator.ReplaceFenced(ctx, namespace, []byte("server static canary"), func(
		ctx context.Context,
		transaction *sql.Tx,
		update AuthorityUpdate,
	) (string, error) {
		require.Equal(t, testOwnerID, update.Owner)
		require.Equal(t, RecordStaticCredential, update.Kind)
		require.NotNil(t, update.Handle)
		var revision int64
		require.NoError(t, transaction.QueryRowContext(ctx, `
			UPDATE server_credentials SET revision = revision + 1, handle = ?
			WHERE server_id = ? AND kind = ? AND revision = 0
			RETURNING revision`, string(*update.Handle), update.Owner, update.Kind).Scan(&revision))
		return strconv.FormatInt(revision, 10), nil
	})
	require.NoError(t, err)
	assert.Equal(t, "1", result.Revision)

	active, metadata, err := coordinator.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, []byte("server static canary"), active)
	assert.Equal(t, result, metadata)
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		var revision int64
		var handle string
		return transaction.QueryRowContext(ctx, `
			SELECT revision, handle FROM server_credentials
			WHERE server_id = ? AND kind = ?`, testOwnerID, RecordStaticCredential).Scan(&revision, &handle)
	}))
}

func TestFencedInvalidationCommitsNonauthorityBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	coordinator := NewCoordinator(
		provider,
		store,
		testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)),
		testutil.NewFakeEntropy(uniqueEntropy(1)),
	)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))

	_, err = coordinator.ReplaceFenced(ctx, namespace, []byte("old token set"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	invalidated, err := coordinator.InvalidateFenced(ctx, namespace, credentialDomainCallback(t, 1))
	require.NoError(t, err)
	assert.Equal(t, "2", invalidated.Revision)
	assert.Empty(t, invalidated.Handle)
	_, _, err = coordinator.ReadActive(ctx, namespace)
	assert.ErrorIs(t, err, ErrNoAuthority)
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		var revision int64
		var handle sql.NullString
		if err := transaction.QueryRowContext(ctx, `
			SELECT revision, handle FROM server_credentials
			WHERE server_id = ? AND kind = ?`, namespace.Owner(), namespace.Kind()).Scan(&revision, &handle); err != nil {
			return err
		}
		assert.Equal(t, int64(2), revision)
		assert.False(t, handle.Valid)
		return nil
	}))
	status, err := coordinator.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.InUse)
	assert.Empty(t, adapter.values())
}

func TestPostAuthorizationSuccessInstallFailureInvalidatesOldAndCandidate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	coordinator := NewCoordinator(
		provider,
		store,
		testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)),
		testutil.NewFakeEntropy(uniqueEntropy(2)),
	)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	_, err = coordinator.ReplaceFenced(ctx, namespace, []byte("old token set"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	adapter.failSetAt = adapter.setCalls + 1

	_, err = coordinator.ReplaceFencedAfterAuthorizationSuccess(
		ctx, namespace, []byte("new token set"), credentialDomainCallback(t, 1),
	)
	require.Error(t, err)
	_, _, readErr := coordinator.ReadActive(ctx, namespace)
	assert.ErrorIs(t, readErr, ErrNoAuthority)
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		var revision int64
		var handle sql.NullString
		if queryErr := transaction.QueryRowContext(ctx, `
			SELECT revision, handle FROM server_credentials
			WHERE server_id = ? AND kind = ?`, namespace.Owner(), namespace.Kind()).Scan(&revision, &handle); queryErr != nil {
			return queryErr
		}
		assert.Equal(t, int64(2), revision)
		assert.False(t, handle.Valid)
		return nil
	}))
	assert.Empty(t, adapter.values())
}

func TestPostAuthorizationSuccessPostCommitFailureInvalidatesPublishedCandidate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	_, err = baseline.ReplaceFenced(ctx, namespace, []byte("old token set"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	candidate := newCoordinatorWithHooks(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{0x51}, generationEntropyBytes)),
		cutoverHooks{afterCommit: injectedCrash},
	)

	_, err = candidate.ReplaceFencedAfterAuthorizationSuccess(
		ctx, namespace, []byte("new token set"), credentialDomainCallback(t, 1),
	)
	assert.ErrorIs(t, err, errInjectedCrash)
	_, _, readErr := baseline.ReadActive(ctx, namespace)
	assert.ErrorIs(t, readErr, ErrNoAuthority)
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		var revision int64
		var handle sql.NullString
		if queryErr := transaction.QueryRowContext(ctx, `
			SELECT revision, handle FROM server_credentials
			WHERE server_id = ? AND kind = ?`, namespace.Owner(), namespace.Kind()).Scan(&revision, &handle); queryErr != nil {
			return queryErr
		}
		assert.Equal(t, int64(3), revision)
		assert.False(t, handle.Valid)
		return nil
	}))
}

func TestOrdinaryCallbackFailureKeepsOldAuthorityAndCleansCandidate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	old, err := baseline.ReplaceFenced(ctx, namespace, []byte("old authority"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	candidate := NewCoordinator(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{0x61}, generationEntropyBytes)),
	)

	_, err = candidate.ReplaceFenced(ctx, namespace, []byte("new authority"), func(
		context.Context, *sql.Tx, AuthorityUpdate,
	) (string, error) {
		return "", errInjectedCrash
	})
	assert.ErrorIs(t, err, errInjectedCrash)
	active, metadata, err := baseline.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, []byte("old authority"), active)
	assert.Equal(t, old, metadata)
	status, err := baseline.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.InUse)
	for item := range adapter.values() {
		assert.Contains(t, item, string(old.Handle))
	}
}

func TestPostAuthorizationSuccessCallbackFailureLeavesNoAuthority(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	_, err = baseline.ReplaceFenced(ctx, namespace, []byte("old token set"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	candidate := NewCoordinator(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{0x62}, generationEntropyBytes)),
	)
	invalidate := credentialDomainCallback(t, 1)
	callback := func(ctx context.Context, transaction *sql.Tx, update AuthorityUpdate) (string, error) {
		if update.Handle != nil {
			return "", errInjectedCrash
		}
		return invalidate(ctx, transaction, update)
	}

	_, err = candidate.ReplaceFencedAfterAuthorizationSuccess(ctx, namespace, []byte("new token set"), callback)
	assert.ErrorIs(t, err, errInjectedCrash)
	_, _, readErr := baseline.ReadActive(ctx, namespace)
	assert.ErrorIs(t, readErr, ErrNoAuthority)
	status, err := baseline.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.InUse)
	assert.Empty(t, adapter.values())
}

func TestFencedKindsAdvanceIndependentDomainRevisions(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	coordinator := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(4)))
	firstNamespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, firstNamespace))

	for _, kind := range []RecordKind{RecordStaticCredential, RecordOAuthClient, RecordOAuthTokens} {
		namespace, namespaceErr := NewNamespace(testInstallationID, testOwnerID, kind)
		require.NoError(t, namespaceErr)
		result, replaceErr := coordinator.ReplaceFenced(ctx, namespace, []byte("complete "+string(kind)), credentialDomainCallback(t, 0))
		require.NoError(t, replaceErr)
		assert.Equal(t, "1", result.Revision)
	}
	staticResult, err := coordinator.ReplaceFenced(ctx, firstNamespace, []byte("static replacement"), credentialDomainCallback(t, 1))
	require.NoError(t, err)
	assert.Equal(t, "2", staticResult.Revision)
	_, activeMetadata, err := coordinator.ReadActive(ctx, firstNamespace)
	require.NoError(t, err)
	assert.Equal(t, "2", activeMetadata.Revision)
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		rows, queryErr := transaction.QueryContext(ctx, `
			SELECT kind, revision FROM server_credentials
			WHERE server_id = ? ORDER BY kind`, testOwnerID)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		count := 0
		for rows.Next() {
			var kind string
			var revision int64
			if scanErr := rows.Scan(&kind, &revision); scanErr != nil {
				return scanErr
			}
			if kind == string(RecordStaticCredential) {
				assert.Equal(t, int64(2), revision, kind)
			} else {
				assert.Equal(t, int64(1), revision, kind)
			}
			count++
		}
		assert.Equal(t, 3, count)
		return rows.Err()
	}))
}

func TestFencedCallbackCrashKeepsGenerationAndDomainMetadataOldOrNew(t *testing.T) {
	for name, hooks := range map[string]cutoverHooks{
		"before commit": {beforeCommit: injectedCrash},
		"after commit":  {afterCommit: injectedCrash},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newCutoverStore(t)
			adapter := newMemoryAdapter()
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)
			clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
			namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			require.NoError(t, insertCredentialDomain(t, store, namespace))
			baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
			old, err := baseline.ReplaceFenced(ctx, namespace, []byte("old authority"), credentialDomainCallback(t, 0))
			require.NoError(t, err)
			candidate := newCoordinatorWithHooks(
				provider,
				store,
				clock,
				testutil.NewFakeEntropy(bytes.Repeat([]byte{0x63}, generationEntropyBytes)),
				hooks,
			)

			result, err := candidate.ReplaceFenced(ctx, namespace, []byte("new authority"), credentialDomainCallback(t, 1))
			assert.ErrorIs(t, err, errInjectedCrash)
			active, metadata, err := baseline.ReadActive(ctx, namespace)
			require.NoError(t, err)
			expected := old
			expectedSecret := []byte("old authority")
			if hooks.afterCommit != nil {
				expected = result
				expectedSecret = []byte("new authority")
			}
			assert.Equal(t, expectedSecret, active)
			assert.Equal(t, expected, metadata)
			require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
				var revision int64
				var handle string
				if queryErr := transaction.QueryRowContext(ctx, `
					SELECT revision, handle FROM server_credentials
					WHERE server_id = ? AND kind = ?`, namespace.Owner(), namespace.Kind()).Scan(&revision, &handle); queryErr != nil {
					return queryErr
				}
				assert.Equal(t, expected.Revision, strconv.FormatInt(revision, 10))
				assert.Equal(t, string(expected.Handle), handle)
				return nil
			}))
			require.NoError(t, baseline.CleanupCandidates(ctx, namespace))
		})
	}
}

func TestCandidateCutoverCrashPointsKeepOldOrNewCompleteAuthority(t *testing.T) {
	for name, hooks := range map[string]cutoverHooks{
		"after candidate registration": {afterCandidate: injectedCrash},
		"after keyring write":          {afterWrite: injectedCrash},
		"before SQLite commit":         {beforeCommit: injectedCrash},
		"after SQLite commit":          {afterCommit: injectedCrash},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newCutoverStore(t)
			adapter := newMemoryAdapter()
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)
			clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
			entropy := testutil.NewFakeEntropy(uniqueEntropy(3))
			namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			baseline := NewCoordinator(provider, store, clock, entropy)
			oldSecret := []byte("old complete authority")
			_, err = baseline.Replace(ctx, namespace, oldSecret)
			require.NoError(t, err)

			candidate := newCoordinatorWithHooks(provider, store, clock, entropy, hooks)
			newSecret := []byte("new complete authority")
			_, replaceErr := candidate.Replace(ctx, namespace, newSecret)
			require.ErrorIs(t, replaceErr, errInjectedCrash)

			active, _, err := candidate.ReadActive(ctx, namespace)
			require.NoError(t, err)
			if hooks.afterCommit != nil {
				assert.Equal(t, newSecret, active)
			} else {
				assert.Equal(t, oldSecret, active)
			}
			status, err := candidate.CandidateStatus(ctx)
			require.NoError(t, err)
			assert.LessOrEqual(t, status.InUse, status.Limit)
			require.NoError(t, candidate.CleanupCandidates(ctx, namespace))
			status, err = candidate.CandidateStatus(ctx)
			require.NoError(t, err)
			assert.Zero(t, status.InUse)
		})
	}
}

func TestCutoverAuthorityRemainsOldOrNewAfterStoreReopen(t *testing.T) {
	for name, hooks := range map[string]cutoverHooks{
		"before commit": {beforeCommit: injectedCrash},
		"after commit":  {afterCommit: injectedCrash},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0o700))
			ownership, err := gatewaypaths.AcquireForMaintenance(root)
			require.NoError(t, err)
			store, err := storage.Initialize(ctx, ownership, testInstallationID)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, store.Close())
				require.NoError(t, ownership.Close())
			})
			adapter := newMemoryAdapter()
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)
			clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
			entropy := testutil.NewFakeEntropy(uniqueEntropy(3))
			namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			baseline := NewCoordinator(provider, store, clock, entropy)
			oldSecret := []byte("old reopened authority")
			_, err = baseline.Replace(ctx, namespace, oldSecret)
			require.NoError(t, err)
			candidate := newCoordinatorWithHooks(provider, store, clock, entropy, hooks)
			newSecret := []byte("new reopened authority")
			_, err = candidate.Replace(ctx, namespace, newSecret)
			assert.ErrorIs(t, err, errInjectedCrash)

			require.NoError(t, store.Close())
			store, err = storage.Open(ctx, ownership)
			require.NoError(t, err)
			restarted := NewCoordinator(provider, store, clock, entropy)
			active, _, err := restarted.ReadActive(ctx, namespace)
			require.NoError(t, err)
			if hooks.afterCommit != nil {
				assert.Equal(t, newSecret, active)
			} else {
				assert.Equal(t, oldSecret, active)
			}
			require.NoError(t, restarted.CleanupCandidates(ctx, namespace))
		})
	}
}

func TestLateDesiredFenceRejectsCandidateAndPreservesOldAuthority(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	old, err := baseline.ReplaceFenced(ctx, namespace, []byte("old authority"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	candidate := newCoordinatorWithHooks(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{0x71}, generationEntropyBytes)),
		cutoverHooks{afterWrite: func() error {
			return store.Mutate(ctx, func(transaction *sql.Tx) error {
				_, updateErr := transaction.ExecContext(ctx, `
					UPDATE servers SET desired_revision = desired_revision + 1 WHERE id = ?`, namespace.Owner())
				return updateErr
			})
		}},
	)
	callback := func(ctx context.Context, transaction *sql.Tx, update AuthorityUpdate) (string, error) {
		var desiredRevision int64
		if queryErr := transaction.QueryRowContext(ctx, `
			SELECT desired_revision FROM servers WHERE id = ?`, update.Owner).Scan(&desiredRevision); queryErr != nil {
			return "", queryErr
		}
		if desiredRevision != 1 {
			return "", errLateFence
		}
		return credentialDomainCallback(t, 1)(ctx, transaction, update)
	}

	_, err = candidate.ReplaceFenced(ctx, namespace, []byte("late authority"), callback)
	assert.ErrorIs(t, err, errLateFence)
	active, metadata, err := baseline.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, []byte("old authority"), active)
	assert.Equal(t, old, metadata)
	status, err := baseline.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.InUse)
}

func TestLateAuthorityInvalidationRejectsCandidate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordOAuthTokens)
	require.NoError(t, err)
	require.NoError(t, insertCredentialDomain(t, store, namespace))
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	_, err = baseline.ReplaceFenced(ctx, namespace, []byte("old token set"), credentialDomainCallback(t, 0))
	require.NoError(t, err)
	candidate := newCoordinatorWithHooks(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{0x72}, generationEntropyBytes)),
		cutoverHooks{afterWrite: func() error {
			_, invalidateErr := baseline.InvalidateFenced(ctx, namespace, credentialDomainCallback(t, 1))
			return invalidateErr
		}},
	)

	_, err = candidate.ReplaceFenced(ctx, namespace, []byte("late token set"), credentialDomainCallback(t, 1))
	require.Error(t, err)
	_, _, readErr := baseline.ReadActive(ctx, namespace)
	assert.ErrorIs(t, readErr, ErrNoAuthority)
	status, err := baseline.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.InUse)
	assert.Empty(t, adapter.values())
}

func TestDrainFencesGenerationThatReturnsAfterBackendWork(t *testing.T) {
	for name, configure := range map[string]func(*memoryAdapter, chan struct{}, chan struct{}){
		"set": func(adapter *memoryAdapter, started, release chan struct{}) {
			adapter.blockSetAt = 1
			adapter.setStarted = started
			adapter.releaseSet = release
		},
		"get": func(adapter *memoryAdapter, started, release chan struct{}) {
			adapter.blockGetAt = 1
			adapter.getStarted = started
			adapter.releaseGet = release
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newCutoverStore(t)
			started := make(chan struct{})
			release := make(chan struct{})
			adapter := newMemoryAdapter()
			configure(adapter, started, release)
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)
			clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
			coordinator := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(2)))
			namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			result := make(chan error, 1)
			go func() {
				_, replaceErr := coordinator.Replace(ctx, namespace, []byte("late candidate secret"))
				result <- replaceErr
			}()
			<-started

			_, _, err = coordinator.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, ErrWorkLimit)
			coordinator.Drain()
			assert.Equal(t, contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}, provider.WorkStatus())
			close(release)
			assert.ErrorIs(t, <-result, ErrDraining)

			identity, err := store.Identity(ctx)
			require.NoError(t, err)
			assert.Zero(t, identity.Revision)
			_, _, err = coordinator.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, ErrDraining)
			status, err := coordinator.CandidateStatus(ctx)
			require.NoError(t, err)
			assert.Equal(t, int64(1), status.InUse)

			restarted := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
			_, _, err = restarted.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, ErrNoAuthority)
			require.NoError(t, restarted.CleanupCandidates(ctx, namespace))
			status, err = restarted.CandidateStatus(ctx)
			require.NoError(t, err)
			assert.Zero(t, status.InUse)
			assert.Empty(t, adapter.values())
		})
	}
}

func TestStorageLatchFencesActiveReadThatReturnsLate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	coordinator := NewCoordinator(
		provider,
		store,
		testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)),
		testutil.NewFakeEntropy(uniqueEntropy(1)),
	)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	_, err = coordinator.Replace(ctx, namespace, []byte("current authority"))
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter.blockNextGet(started, release)
	result := make(chan error, 1)
	go func() {
		_, _, readErr := coordinator.ReadActive(ctx, namespace)
		result <- readErr
	}()
	<-started

	err = store.Mutate(ctx, func(*sql.Tx) error { return sqlite3.IOERR })
	assert.ErrorIs(t, err, storage.ErrStorageLatched)
	close(release)
	assert.ErrorIs(t, <-result, storage.ErrStorageLatched)
}

func TestDrainFencesActiveReadThatReturnsLate(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	coordinator := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	_, err = coordinator.Replace(ctx, namespace, []byte("current authority"))
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter.blockNextGet(started, release)
	result := make(chan error, 1)
	go func() {
		_, _, readErr := coordinator.ReadActive(ctx, namespace)
		result <- readErr
	}()
	<-started

	coordinator.Drain()
	close(release)
	assert.ErrorIs(t, <-result, ErrDraining)
	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), identity.Revision)
}

func TestDrainDuringCandidateDeleteCannotChangeAuthority(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	baseline := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(1)))
	oldSecret := []byte("current authority")
	old, err := baseline.Replace(ctx, namespace, oldSecret)
	require.NoError(t, err)
	candidate, err := NewHandle(bytes.NewReader(bytes.Repeat([]byte{9}, generationEntropyBytes)))
	require.NoError(t, err)
	require.NoError(t, baseline.registerCandidate(ctx, namespace, candidate))
	require.NoError(t, provider.WriteGeneration(ctx, namespace, candidate, []byte("cleanup candidate")))

	started := make(chan struct{})
	release := make(chan struct{})
	adapter.blockDeleteAt = 1
	adapter.deleteStarted = started
	adapter.releaseDelete = release
	entropy := testutil.NewFakeEntropy(uniqueEntropy(1))
	replacement := NewCoordinator(provider, store, clock, entropy)
	result := make(chan error, 1)
	go func() {
		_, replaceErr := replacement.Replace(ctx, namespace, []byte("must not commit"))
		result <- replaceErr
	}()
	<-started

	replacement.Drain()
	assert.True(t, provider.WorkStatus().Saturated)
	close(release)
	assert.ErrorIs(t, <-result, ErrDraining)
	active, metadata, err := baseline.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, oldSecret, active)
	assert.Equal(t, old, metadata)
	assert.Equal(t, generationEntropyBytes, entropy.Remaining())
}

func TestSuccessfulCutoverAdvancesRevisionAndLeavesOnlyNewGeneration(t *testing.T) {
	ctx := context.Background()
	store, root := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	coordinator := NewCoordinator(provider, store, clock, testutil.NewFakeEntropy(uniqueEntropy(2)))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)

	firstSecret := []byte("keyring-canary-first")
	first, err := coordinator.Replace(ctx, namespace, firstSecret)
	require.NoError(t, err)
	assert.Equal(t, "1", first.Revision)
	secondSecret := []byte("keyring-canary-second")
	second, err := coordinator.Replace(ctx, namespace, secondSecret)
	require.NoError(t, err)
	assert.Equal(t, "2", second.Revision)
	assert.NotEqual(t, first.Handle, second.Handle)

	active, metadata, err := coordinator.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, secondSecret, active)
	assert.Equal(t, second, metadata)
	require.NoError(t, store.BackupTo(ctx, filepath.Join(root, "authority-backup.db")))
	for _, canary := range [][]byte{firstSecret, secondSecret} {
		assertCanaryAbsentFromDataRoot(t, root, canary)
	}
	for item := range adapter.values() {
		assert.Contains(t, item, string(second.Handle))
		assert.NotContains(t, item, string(first.Handle))
	}
}

func TestReplacementRejectsActiveHandleCollisionBeforeKeyringWrite(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	coordinator := NewCoordinator(
		provider,
		store,
		clock,
		testutil.NewFakeEntropy(bytes.Repeat([]byte{7}, generationEntropyBytes*2)),
	)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	oldSecret := []byte("old authority")
	first, err := coordinator.Replace(ctx, namespace, oldSecret)
	require.NoError(t, err)
	itemsBefore := adapter.values()

	_, err = coordinator.Replace(ctx, namespace, []byte("replacement authority"))

	assert.ErrorIs(t, err, ErrHandleCollision)
	active, metadata, err := coordinator.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, oldSecret, active)
	assert.Equal(t, first, metadata)
	assert.Equal(t, itemsBefore, adapter.values())
}

func TestCutoverRejectsForeignInstallationBeforeEntropyOrBackendWork(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	entropy := testutil.NewFakeEntropy(uniqueEntropy(1))
	coordinator := NewCoordinator(
		provider,
		store,
		testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)),
		entropy,
	)
	foreign, err := NewNamespace("01ARZ3NDEKTSV4RRFFQ69G5FAY", testOwnerID, RecordStaticCredential)
	require.NoError(t, err)

	_, err = coordinator.Replace(ctx, foreign, []byte("must not be written"))

	require.Error(t, err)
	assert.Equal(t, generationEntropyBytes, entropy.Remaining())
	assert.Empty(t, adapter.values())
}

func TestCandidateRegistryRejectsNPlusOneWithoutEntropyOrKeyringWork(t *testing.T) {
	ctx := context.Background()
	store, _ := newCutoverStore(t)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
	entropy := testutil.NewFakeEntropy(uniqueEntropy(1))
	coordinator := NewCoordinator(provider, store, clock, entropy)
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	limit := int(mustFixedLimit("keyring_candidates"))

	for index := range limit {
		handle, handleErr := NewHandle(bytes.NewReader(bytes.Repeat([]byte{byte(index)}, generationEntropyBytes)))
		require.NoError(t, handleErr)
		require.NoError(t, coordinator.registerCandidate(ctx, namespace, handle))
	}
	extra, err := NewHandle(bytes.NewReader(bytes.Repeat([]byte{byte(limit)}, generationEntropyBytes)))
	require.NoError(t, err)

	err = coordinator.registerCandidate(ctx, namespace, extra)

	assert.ErrorIs(t, err, ErrCandidateLimit)
	assert.Equal(t, generationEntropyBytes, entropy.Remaining())
	assert.Empty(t, adapter.values())
	status, err := coordinator.CandidateStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(limit), status.InUse)
	assert.True(t, status.Saturated)
}

func credentialDomainCallback(t *testing.T, expectedRevision int64) AuthorityCallback {
	t.Helper()
	return func(ctx context.Context, transaction *sql.Tx, update AuthorityUpdate) (string, error) {
		var revision int64
		var handle any
		if update.Handle != nil {
			handle = string(*update.Handle)
		}
		expected := expectedRevision
		if update.PriorPublishedRevision != "" {
			parsed, err := strconv.ParseInt(update.PriorPublishedRevision, 10, 64)
			if err != nil {
				return "", err
			}
			expected = parsed
		}
		if update.ValidateOnly {
			if err := transaction.QueryRowContext(ctx, `
				SELECT revision FROM server_credentials
				WHERE server_id = ? AND kind = ?`, update.Owner, update.Kind).Scan(&revision); err != nil {
				return "", err
			}
			if revision != expected {
				return "", sql.ErrNoRows
			}
			return strconv.FormatInt(revision, 10), nil
		}
		if err := transaction.QueryRowContext(ctx, `
			UPDATE server_credentials SET revision = revision + 1, handle = ?
			WHERE server_id = ? AND kind = ? AND revision = ?
			RETURNING revision`, handle, update.Owner, update.Kind, expected).Scan(&revision); err != nil {
			return "", err
		}
		return strconv.FormatInt(revision, 10), nil
	}
}

func insertCredentialDomain(t *testing.T, store *storage.Store, namespace Namespace) error {
	t.Helper()
	return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err := transaction.Exec(`
			INSERT INTO server_identities (id, namespace, created_at)
			VALUES (?, 'keyring-test', ?)`, namespace.Owner(), now); err != nil {
			return err
		}
		if _, err := transaction.Exec(`
			INSERT INTO servers (
				id, display_name, desired_state, desired_revision, transport_json,
				created_at, updated_at, deleted_at
			) VALUES (?, 'Keyring Test', 'disabled', 1, '{}', ?, ?, NULL)`, namespace.Owner(), now, now); err != nil {
			return err
		}
		for _, kind := range []RecordKind{RecordStaticCredential, RecordOAuthClient, RecordOAuthTokens} {
			if _, err := transaction.Exec(`
				INSERT INTO server_credentials (server_id, kind, revision, handle)
				VALUES (?, ?, 0, NULL)`, namespace.Owner(), kind); err != nil {
				return err
			}
		}
		_, err := transaction.Exec(`
			INSERT INTO server_oauth_registrations (server_id, revision)
			VALUES (?, 0)`, namespace.Owner())
		return err
	})
}

func newCutoverStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	ownership, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, testInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		require.NoError(t, ownership.Close())
	})
	return store, root
}

func uniqueEntropy(count int) []byte {
	entropy := make([]byte, count*generationEntropyBytes)
	for index := range entropy {
		entropy[index] = byte(index)
	}
	return entropy
}

var (
	errInjectedCrash = errors.New("injected crash")
	errLateFence     = errors.New("late desired fence")
)

func injectedCrash() error { return errInjectedCrash }

func assertCanaryAbsentFromDataRoot(t *testing.T, root string, canary []byte) {
	t.Helper()
	scanner, err := testutil.NewCanaryScanner(canary)
	require.NoError(t, err)
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || !info.Mode().IsRegular() {
			return walkErr
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		scanErr := scanner.Scan("Gateway data file", file)
		return errors.Join(scanErr, file.Close())
	}))
}
