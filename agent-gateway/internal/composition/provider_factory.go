//go:build !e2e

package composition

import "github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"

func productionProvider(installationID string) (*keyring.Provider, error) {
	return keyring.NewProvider(installationID)
}
