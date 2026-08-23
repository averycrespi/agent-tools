package keyring

import (
	"io"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type MemoryAdapterForTest = memoryAdapter

func NewMemoryAdapterForTest() *MemoryAdapterForTest {
	return newMemoryAdapter()
}

func NewProviderForTest(installationID string, backend *MemoryAdapterForTest) (*Provider, error) {
	return newProviderWithAdapter(installationID, backend)
}

func (adapter *memoryAdapter) BlockNextSetForTest(started, release chan struct{}) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.blockSetAt = adapter.setCalls + 1
	adapter.setStarted = started
	adapter.releaseSet = release
}

func (coordinator *Coordinator) ProviderForTest() *Provider {
	return coordinator.provider
}

func NewCoordinatorWithAfterCommitForTest(
	provider *Provider,
	store *storage.Store,
	clock Clock,
	entropy io.Reader,
	afterCommit func() error,
) *Coordinator {
	return newCoordinatorWithHooks(provider, store, clock, entropy, cutoverHooks{afterCommit: afterCommit})
}
