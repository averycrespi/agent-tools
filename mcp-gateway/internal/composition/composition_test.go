package composition

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compositionTime = time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)

func TestNewBuildsOneFailClosedProductionGraph(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()

	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	require.NotNil(t, built.servers)
	require.NotNil(t, built.catalogRepository)
	require.NotNil(t, built.activeCatalog)
	require.NotNil(t, built.traverser)
	require.NotNil(t, built.remoteFactory)
	require.NotNil(t, built.provider)
	require.NotNil(t, built.keyring)
	require.NotNil(t, built.authority)
	require.NotNil(t, built.owner)
	require.NotNil(t, built.stdio)
	require.NotNil(t, built.driver)
	require.NotNil(t, built.catalog)
	require.NotNil(t, built.oauthResolver)
	require.NotNil(t, built.disconnect)
	require.NotNil(t, built.registrar)
	require.NotNil(t, built.flows)
	require.NotNil(t, built.refresh)
	require.NotNil(t, built.replacements)
	require.NotNil(t, built.manager)
	require.NotNil(t, built.publisher)
	assert.Same(t, built.activeCatalog, built.publisher.active)
	assert.False(t, built.callbacks.bound())

	candidate := runtimes.Candidate{}
	assert.False(t, built.callbacks.current(candidate))
	_, ok := built.callbacks.client(candidate)
	assert.False(t, ok)
	assert.False(t, built.callbacks.report(candidate, runtimes.FailureDisposition{}))
	assert.False(t, built.callbacks.complete(candidate, runtimes.CatalogOutcome{}, nil))
	assert.False(t, built.callbacks.running())
	candidate.Server.ID = "server"
	candidate.Generation = 1
	assert.True(t, built.publisher.current(candidate))
	built.publisher.Fence("server", 2)
	assert.False(t, built.publisher.current(candidate))
	built.callbacks.state("server", contract.ServerCredentialReady, true)
	built.callbacks.trigger("server")
	built.callbacks.fence("server")
}

func TestNewFailsEveryMandatoryConstructor(t *testing.T) {
	for _, stage := range mandatoryConstructorStages {
		t.Run(stage, func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			built, err := newWithHooks(options, constructorHooks{before: func(current string) error {
				if current == stage {
					return errors.New("injected constructor failure")
				}
				return nil
			}})
			require.Error(t, err)
			assert.Nil(t, built)
			assert.True(t, strings.Contains(err.Error(), stage), err.Error())
		})
	}
}

func newCompositionOptions(t *testing.T) (Options, func()) {
	t.Helper()
	dataDir := t.TempDir()
	require.NoError(t, os.Chmod(dataDir, 0o700))
	ownership, err := gatewaypaths.Acquire(dataDir)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	identity, err := store.Identity(context.Background())
	require.NoError(t, err)
	entropy := make([]byte, 8192)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	return Options{
			Store:          store,
			InstallationID: identity.InstallationID,
			CallbackURL:    "http://127.0.0.1:47100/oauth/callback",
			Clock:          testutil.NewFakeClock(compositionTime),
			Entropy:        testutil.NewFakeEntropy(entropy),
			Invalidate:     func(contract.Invalidation) {},
		}, func() {
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
		}
}
