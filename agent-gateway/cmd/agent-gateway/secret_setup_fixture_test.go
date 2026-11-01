package main

import (
	"crypto/rand"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

// Stopped custody setup needs a real pre-custody installation, not a production
// override that permits unencrypted new installations.
func initializeUnconfiguredCustodyFixture(t *testing.T, root, bearerPath string) {
	t.Helper()
	owner, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	clock := systemClock{}
	id, err := admin.NewID(clock.Now(), rand.Reader)
	require.NoError(t, err)
	store, err := storage.Initialize(t.Context(), owner, id)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	generation, err := admin.NewID(clock.Now(), rand.Reader)
	require.NoError(t, err)
	require.NoError(t, composition.InitializeTraffic(t.Context(), owner, store, generation))
	_, err = admin.NewService(store, clock, rand.Reader).Initialize(t.Context(), admin.NewFileSecretSink(bearerPath))
	require.NoError(t, err)
}
