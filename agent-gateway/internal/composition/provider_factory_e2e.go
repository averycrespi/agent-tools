//go:build e2e

package composition

import (
	"context"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
)

// e2eMaterialDirectory is set only by the isolated real-binary harness at link
// time. Normal E2E/demo builds retain process-local material. It is not a runtime
// flag, environment fallback, native provider or production persistence claim.
var e2eMaterialDirectory string

func productionProvider(installationID string) (*keyring.Provider, error) {
	if e2eMaterialDirectory != "" {
		backend, err := newE2EFileBackend(e2eMaterialDirectory)
		if err != nil {
			return nil, err
		}
		return keyring.NewProviderWithBackend(installationID, backend)
	}
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
