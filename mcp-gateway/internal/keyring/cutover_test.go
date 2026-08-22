package keyring

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

var errInjectedCrash = errors.New("injected crash")

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
