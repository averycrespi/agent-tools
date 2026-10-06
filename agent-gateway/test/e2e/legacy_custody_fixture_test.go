//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func newLegacyGatewayHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	return newGatewayHarnessCustody(t, context.Background(), gatewayBinary(t), true)
}

type legacyCustodyBackend map[string]string

func (legacyCustodyBackend) Probe(context.Context, string) error { return nil }
func (b legacyCustodyBackend) Set(service, item, value string) error {
	b[service+item] = value
	return nil
}
func (b legacyCustodyBackend) Get(service, item string) (string, error) {
	value, ok := b[service+item]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (b legacyCustodyBackend) Delete(service, item string) error { delete(b, service+item); return nil }

// Seed historical custody through domain owners, without invoking new-installation
// init or disabling its production backup refusal. These MCP-only fixtures qualify
// legacy restore behavior; they never grant native-resource qualification.
func initializeLegacyGatewayFixture(t *testing.T, root, secretPath string) {
	t.Helper()
	owner, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	clock := e2eClock{}
	id, err := admin.NewID(clock.Now(), rand.Reader)
	require.NoError(t, err)
	store, err := storage.Initialize(t.Context(), owner, id)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	generation, err := admin.NewID(clock.Now(), rand.Reader)
	require.NoError(t, err)
	require.NoError(t, composition.InitializeTraffic(t.Context(), owner, store, generation))
	_, err = admin.NewService(store, clock, rand.Reader).Initialize(t.Context(), admin.NewFileSecretSink(secretPath))
	require.NoError(t, err)
	provider, err := keyring.NewProviderWithBackend(id, legacyCustodyBackend{})
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(provider, store, clock, rand.Reader)
	ca, err := httpca.New(store, coordinator, id, clock, rand.Reader)
	require.NoError(t, err)
	defer ca.Close()
	require.NoError(t, ca.Replace(t.Context(), "0"))
	certificate, _, err := ca.PublicCertificate(t.Context())
	require.NoError(t, err)
	require.NoError(t, gatewaypaths.PublishCertificate(filepath.Join(root, gatewaypaths.PublicCertificateName), certificate, nil))
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) SELECT handle,owner,kind,'legacy' FROM keyring_authorities`)
		return err
	}))
}
