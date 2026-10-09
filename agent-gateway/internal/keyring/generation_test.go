package keyring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCompleteEncryptedGenerationRoundTripAndDeletion(t *testing.T) {
	for _, size := range []int{1, 2240, 2241, secretMaximumBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			_, _, provider := custodyFixture(t)
			ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
			require.NoError(t, err)
			handle, err := NewHandle(bytes.NewReader(make([]byte, generationEntropyBytes)))
			require.NoError(t, err)
			secret := bytes.Repeat([]byte("s"), size)
			require.NoError(t, provider.WriteGeneration(t.Context(), ns, handle, secret))
			loaded, err := provider.ReadGeneration(t.Context(), ns, handle)
			require.NoError(t, err)
			require.Equal(t, secret, loaded)
			require.NoError(t, provider.DeleteGeneration(t.Context(), ns, handle))
			_, err = provider.ReadGeneration(t.Context(), ns, handle)
			require.ErrorIs(t, err, ErrIncompleteGeneration)
		})
	}
}

func TestEncryptedGenerationRejectsOversizeAndUnconfiguredProvider(t *testing.T) {
	_, _, provider := custodyFixture(t)
	ns, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	handle, err := NewHandle(bytes.NewReader(make([]byte, generationEntropyBytes)))
	require.NoError(t, err)
	require.ErrorIs(t, provider.WriteGeneration(t.Context(), ns, handle, make([]byte, secretMaximumBytes+1)), ErrSecretTooLarge)
	_, err = provider.ReadGeneration(t.Context(), ns, handle)
	require.ErrorIs(t, err, ErrIncompleteGeneration)
	unconfigured, err := NewProvider(testInstallationID)
	require.NoError(t, err)
	require.ErrorIs(t, unconfigured.WriteGeneration(t.Context(), ns, handle, []byte("secret")), ErrCustodyUnavailable)
	_, err = unconfigured.ReadGeneration(t.Context(), ns, handle)
	require.ErrorIs(t, err, ErrCustodyUnavailable)
	require.ErrorIs(t, unconfigured.DeleteGeneration(t.Context(), ns, handle), ErrCustodyUnavailable)
}

// This test-only generation store injects completion ordering and failures in
// coordinator tests. It implements no native item, manifest or chunk protocol.
type memoryAdapter struct {
	mu                              sync.Mutex
	items                           map[string]string
	setCalls, failSetAt, blockSetAt int
	setStarted, releaseSet          chan struct{}
	setStartOnce                    sync.Once
	getCalls, blockGetAt            int
	getStarted, releaseGet          chan struct{}
	getStartOnce                    sync.Once
	deleteCalls, blockDeleteAt      int
	deleteStarted, releaseDelete    chan struct{}
	deleteStartOnce                 sync.Once
	probeErr, operationErr          error
}

func newMemoryAdapter() *memoryAdapter { return &memoryAdapter{items: make(map[string]string)} }
func newProviderWithAdapter(id string, store *memoryAdapter) (*Provider, error) {
	provider, err := NewProvider(id)
	if err != nil {
		return nil, err
	}
	provider.custody = store
	provider.work = newWorkLimiter()
	return provider, nil
}
func (adapter *memoryAdapter) capability() Capability {
	if adapter.probeErr != nil {
		return Capability{State: contract.KeyringUnavailable, Remediation: RemediationRetry}
	}
	return Capability{State: contract.KeyringReady, Remediation: RemediationNone}
}
func (adapter *memoryAdapter) write(_ context.Context, _ Namespace, handle Handle, secret []byte) error {
	adapter.mu.Lock()
	adapter.setCalls++
	call := adapter.setCalls
	fail := adapter.failSetAt > 0 && call == adapter.failSetAt
	err := adapter.operationErr
	block := adapter.blockSetAt > 0 && call == adapter.blockSetAt
	started, release := adapter.setStarted, adapter.releaseSet
	adapter.mu.Unlock()
	if fail {
		return errors.New("injected generation write failure")
	}
	if err != nil {
		return err
	}
	if block {
		if started != nil {
			adapter.setStartOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.items[string(handle)] = string(secret)
	return nil
}
func (adapter *memoryAdapter) read(_ context.Context, _ Namespace, handle Handle) ([]byte, error) {
	adapter.mu.Lock()
	adapter.getCalls++
	block := adapter.blockGetAt > 0 && adapter.getCalls == adapter.blockGetAt
	started, release := adapter.getStarted, adapter.releaseGet
	err := adapter.operationErr
	value, ok := adapter.items[string(handle)]
	adapter.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if block {
		if started != nil {
			adapter.getStartOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
	}
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(value), nil
}
func (adapter *memoryAdapter) remove(_ context.Context, _ Namespace, handle Handle) error {
	adapter.mu.Lock()
	adapter.deleteCalls++
	block := adapter.blockDeleteAt > 0 && adapter.deleteCalls == adapter.blockDeleteAt
	started, release := adapter.deleteStarted, adapter.releaseDelete
	err := adapter.operationErr
	adapter.mu.Unlock()
	if err != nil {
		return err
	}
	if block {
		if started != nil {
			adapter.deleteStartOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	delete(adapter.items, string(handle))
	return nil
}
func (adapter *memoryAdapter) blockNextGet(started, release chan struct{}) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.blockGetAt = adapter.getCalls + 1
	adapter.getStarted, adapter.releaseGet = started, release
}
func (adapter *memoryAdapter) values() map[string]string {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	values := make(map[string]string, len(adapter.items))
	for key, value := range adapter.items {
		values[key] = value
	}
	return values
}
