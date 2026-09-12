package keyring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompleteGenerationRoundTripAndDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	for _, size := range []int{1, rawChunkMaximumBytes, rawChunkMaximumBytes + 1, secretMaximumBytes} {
		t.Run(fmt.Sprintf("bytes_%d", size), func(t *testing.T) {
			t.Parallel()
			adapter := newMemoryAdapter()
			provider, err := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, err)
			handle, err := NewHandle(bytes.NewReader(bytes.Repeat([]byte{byte(size)}, generationEntropyBytes)))
			require.NoError(t, err)
			secret := bytes.Repeat([]byte("s"), size)

			require.NoError(t, provider.WriteGeneration(ctx, namespace, handle, secret))
			loaded, err := provider.ReadGeneration(ctx, namespace, handle)
			require.NoError(t, err)
			assert.Equal(t, secret, loaded)
			assert.NotContains(t, string(handle), "ssss")
			for _, value := range adapter.values() {
				assert.LessOrEqual(t, len(value), storedChunkMaximumBytes)
			}

			require.NoError(t, provider.DeleteGeneration(ctx, namespace, handle))
			_, err = provider.ReadGeneration(ctx, namespace, handle)
			assert.ErrorIs(t, err, ErrNotFound)
			assert.Empty(t, adapter.values())
		})
	}
}

func TestGenerationRejectsOversizeBeforeBackendWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	adapter := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	handle, err := NewHandle(bytes.NewReader(make([]byte, generationEntropyBytes)))
	require.NoError(t, err)

	err = provider.WriteGeneration(ctx, namespace, handle, make([]byte, secretMaximumBytes+1))

	assert.ErrorIs(t, err, ErrSecretTooLarge)
	assert.Empty(t, adapter.values())
}

func TestIncompleteOrCorruptGenerationNeverReads(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*memoryAdapter, Namespace, Handle){
		"chunk without manifest": func(adapter *memoryAdapter, namespace Namespace, handle Handle) {
			adapter.put(generationChunkItem(namespace, handle, 0), "c2VjcmV0")
		},
		"missing chunk": func(adapter *memoryAdapter, namespace Namespace, handle Handle) {
			secret := []byte("secret")
			adapter.put(generationManifestItem(namespace, handle), encodeManifest(t, namespace, handle, secret, 1))
		},
		"digest mismatch": func(adapter *memoryAdapter, namespace Namespace, handle Handle) {
			secret := []byte("secret")
			adapter.put(generationManifestItem(namespace, handle), encodeManifest(t, namespace, handle, secret, 1))
			adapter.put(generationChunkItem(namespace, handle, 0), "dGFtcGVyZWQ")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := newMemoryAdapter()
			provider, providerErr := newProviderWithAdapter(testInstallationID, adapter)
			require.NoError(t, providerErr)
			handle, handleErr := NewHandle(bytes.NewReader(bytes.Repeat([]byte{1}, generationEntropyBytes)))
			require.NoError(t, handleErr)
			mutate(adapter, namespace, handle)

			_, readErr := provider.ReadGeneration(ctx, namespace, handle)

			assert.ErrorIs(t, readErr, ErrIncompleteGeneration)
		})
	}
}

func TestWriteFailureDoesNotLeaveReadablePartialGeneration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	adapter := newMemoryAdapter()
	adapter.failSetAt = 2
	provider, err := newProviderWithAdapter(testInstallationID, adapter)
	require.NoError(t, err)
	handle, err := NewHandle(bytes.NewReader(bytes.Repeat([]byte{2}, generationEntropyBytes)))
	require.NoError(t, err)

	err = provider.WriteGeneration(ctx, namespace, handle, bytes.Repeat([]byte("s"), rawChunkMaximumBytes+1))
	require.Error(t, err)
	_, readErr := provider.ReadGeneration(ctx, namespace, handle)
	assert.ErrorIs(t, readErr, ErrNotFound)
}

func encodeManifest(t *testing.T, namespace Namespace, handle Handle, secret []byte, chunks int) string {
	t.Helper()
	digest := sha256.Sum256(secret)
	value, err := marshalGenerationManifest(generationManifest{
		Version: generationFormatVersion,
		Owner:   namespace.Owner(),
		Kind:    string(namespace.Kind()),
		Handle:  string(handle),
		Chunks:  chunks,
		Bytes:   len(secret),
		SHA256:  fmt.Sprintf("%x", digest),
	})
	require.NoError(t, err)
	return value
}

type memoryAdapter struct {
	mu              sync.Mutex
	items           map[string]string
	setCalls        int
	failSetAt       int
	blockSetAt      int
	setStarted      chan struct{}
	releaseSet      chan struct{}
	setStartOnce    sync.Once
	getCalls        int
	blockGetAt      int
	getStarted      chan struct{}
	releaseGet      chan struct{}
	getStartOnce    sync.Once
	deleteCalls     int
	blockDeleteAt   int
	deleteStarted   chan struct{}
	releaseDelete   chan struct{}
	deleteStartOnce sync.Once
	probeErr        error
	operationErr    error
}

func newMemoryAdapter() *memoryAdapter {
	return &memoryAdapter{items: make(map[string]string)}
}

func (adapter *memoryAdapter) Probe(context.Context, string) error { return adapter.probeErr }

func (adapter *memoryAdapter) Set(_, user, password string) error {
	adapter.mu.Lock()
	adapter.setCalls++
	call := adapter.setCalls
	fail := adapter.failSetAt > 0 && call == adapter.failSetAt
	operationErr := adapter.operationErr
	block := adapter.blockSetAt > 0 && call == adapter.blockSetAt
	started := adapter.setStarted
	release := adapter.releaseSet
	adapter.mu.Unlock()
	if fail {
		return errors.New("injected write failure")
	}
	if operationErr != nil {
		return operationErr
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
	adapter.items[user] = password
	return nil
}

func (adapter *memoryAdapter) Get(_, user string) (string, error) {
	adapter.mu.Lock()
	adapter.getCalls++
	block := adapter.blockGetAt > 0 && adapter.getCalls == adapter.blockGetAt
	started := adapter.getStarted
	release := adapter.releaseGet
	operationErr := adapter.operationErr
	value, ok := adapter.items[user]
	adapter.mu.Unlock()
	if operationErr != nil {
		return "", operationErr
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
		return "", ErrNotFound
	}
	return value, nil
}

func (adapter *memoryAdapter) Delete(_, user string) error {
	adapter.mu.Lock()
	adapter.deleteCalls++
	block := adapter.blockDeleteAt > 0 && adapter.deleteCalls == adapter.blockDeleteAt
	started := adapter.deleteStarted
	release := adapter.releaseDelete
	operationErr := adapter.operationErr
	_, ok := adapter.items[user]
	adapter.mu.Unlock()
	if operationErr != nil {
		return operationErr
	}
	if block {
		if started != nil {
			adapter.deleteStartOnce.Do(func() { close(started) })
		}
		if release != nil {
			<-release
		}
	}
	if !ok {
		return ErrNotFound
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	delete(adapter.items, user)
	return nil
}

func (adapter *memoryAdapter) blockNextGet(started, release chan struct{}) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.blockGetAt = adapter.getCalls + 1
	adapter.getStarted = started
	adapter.releaseGet = release
}

func (adapter *memoryAdapter) put(item, value string) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.items[item] = value
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
