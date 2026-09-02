//go:build e2e

package composition

import (
	"bytes"
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2EProviderFactoryPublishesAndReadsExactGeneration(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	provider, err := productionProvider(options.InstallationID)
	require.NoError(t, err)
	assert.Equal(t, contract.KeyringReady, provider.Probe(context.Background()).State)
	coordinator := keyring.NewCoordinator(provider, options.Store, options.Clock, options.Entropy)
	namespace, err := keyring.NewNamespace(options.InstallationID, "01ARZ3NDEKTSV4RRFFQ69G5FAV", keyring.RecordStaticCredential)
	require.NoError(t, err)
	secret := []byte("e2e-generation-canary")
	published, err := coordinator.Replace(context.Background(), namespace, append([]byte(nil), secret...))
	require.NoError(t, err)
	assert.Equal(t, "1", published.Revision)
	active, current, err := coordinator.ReadActive(context.Background(), namespace)
	require.NoError(t, err)
	defer clear(active)
	assert.Equal(t, published.Revision, current.Revision)
	assert.True(t, bytes.Equal(secret, active))
}
