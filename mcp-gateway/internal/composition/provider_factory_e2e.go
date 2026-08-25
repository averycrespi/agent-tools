//go:build e2e

package composition

import (
	"context"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

func productionProvider(installationID string) (*keyring.Provider, error) {
	return keyring.NewProviderWithBackend(installationID, newE2EKeyringBackend())
}

type e2eKeyringBackend struct {
	mu     sync.Mutex
	values map[string]string
}

func newE2EKeyringBackend() *e2eKeyringBackend {
	return &e2eKeyringBackend{values: make(map[string]string)}
}

func (*e2eKeyringBackend) Probe(context.Context, string) error { return nil }

func (backend *e2eKeyringBackend) Set(service, user, password string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.values[service+"\x00"+user] = password
	return nil
}

func (backend *e2eKeyringBackend) Get(service, user string) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	value, ok := backend.values[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (backend *e2eKeyringBackend) Delete(service, user string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	key := service + "\x00" + user
	if _, ok := backend.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(backend.values, key)
	return nil
}
