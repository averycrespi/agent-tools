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

// Historic restore behavior remains qualified with an explicitly legacy fixture,
// not by disabling the encrypted-custody refusal in new-installation production.
func initializeLegacyBackupFixture(t *testing.T, root, bearerPath string) {
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
