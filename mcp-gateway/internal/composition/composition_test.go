package composition

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
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
	require.NotNil(t, built.authorization)
	assert.Same(t, built.authorization, built.Authorization())
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

func TestAuthorityOwnerExposesOccupancyAndDrainsBeforeCompositionCompletes(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	authority := built.Authorization()
	created, err := authority.CreatePrincipal(context.Background(), authorization.CreatePrincipalRequest{
		DisplayName: "composition principal",
		Visibility:  contract.VisibilityAll,
	})
	require.NoError(t, err)
	issued, err := authority.IssueCredential(context.Background(), created.Principal.ID, created.Principal.Revision)
	require.NoError(t, err)
	lease, err := authority.Authenticate(context.Background(), issued.Bearer)
	require.NoError(t, err)
	principals, grants, err := built.AuthorizationOccupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), principals.InUse)
	assert.Equal(t, int64(1), grants.InUse)

	<-built.Drain(context.Background())
	select {
	case <-lease.Done():
	default:
		t.Fatal("composition drain completed before its authority lease was canceled")
	}
	_, err = authority.Authenticate(context.Background(), issued.Bearer)
	assert.ErrorIs(t, err, authorization.ErrShuttingDown)
}

func TestStartRequiresReadinessAndBindsExactlyOnce(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	ready := false
	options.Ready = func() bool { return ready }
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()

	assert.ErrorIs(t, built.Start(context.Background()), ErrNotReady)
	assert.False(t, built.callbacks.bound())
	ready = true
	require.NoError(t, built.Start(context.Background()))
	assert.True(t, built.callbacks.bound())
	assert.True(t, built.callbacks.running())
	assert.ErrorIs(t, built.Start(context.Background()), ErrAlreadyStarted)
}

func TestStartBindsBeforeReconstructionAndIsolatesServerFailure(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	var afterBind, beforeReconstruct bool
	hooks := constructorHooks{startHooks: startHooks{
		afterBind: func(built *Composition) error {
			afterBind = true
			assert.True(t, built.callbacks.bound())
			assert.False(t, built.callbacks.running())
			assert.Equal(t, contract.RuntimeInactive, built.manager.Status("not-started").State)
			return nil
		},
		beforeReconstruct: func(built *Composition) error {
			beforeReconstruct = true
			assert.True(t, built.callbacks.bound())
			assert.False(t, built.callbacks.running())
			return nil
		},
	}}
	built, err := newWithHooks(options, hooks)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	failed := createCompositionServer(t, built.servers, "failed", true, "/definitely/not-an-mcp-server")
	inactive := createCompositionServer(t, built.servers, "inactive", false, "/bin/true")

	require.NoError(t, built.Start(context.Background()))
	assert.True(t, afterBind)
	assert.True(t, beforeReconstruct)
	assert.Equal(t, contract.RuntimeInactive, built.manager.Status(inactive.ID).State)
	require.Eventually(t, func() bool {
		status := built.manager.Status(failed.ID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil
	}, 2*time.Second, time.Millisecond)
}

func TestStartBarrierFailureRunsNoReconstructionAndCannotRetry(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := newWithHooks(options, constructorHooks{startHooks: startHooks{beforeReconstruct: func(*Composition) error {
		return errors.New("blocked before reconstruction")
	}}})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createCompositionServer(t, built.servers, "blocked", true, "/definitely/not-an-mcp-server")

	require.ErrorContains(t, built.Start(context.Background()), "blocked before reconstruction")
	assert.False(t, built.callbacks.running())
	assert.Equal(t, contract.RuntimeInactive, built.manager.Status(server.ID).State)
	assert.Zero(t, built.owner.Status().InUse)
	assert.Equal(t, contract.ActiveCatalogAbsent, built.activeCatalog.Status(server.ID).State)
	assert.ErrorIs(t, built.Start(context.Background()), ErrStartFailed)
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

func TestConstructionRejectsInvalidAuthorizationStateBeforeStartup(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	require.NoError(t, options.Store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`DELETE FROM synthetic_server_identity`)
		return err
	}))

	built, err := New(options)
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorIs(t, err, authorization.ErrInvalidState)
}

func TestConstructionRejectsLatchedAuthorizationBeforeStartup(t *testing.T) {
	options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit {
			return assert.AnError
		}
		return nil
	})
	defer cleanup()
	err := options.Store.Mutate(context.Background(), func(*sql.Tx) error { return nil })
	assert.ErrorIs(t, err, storage.ErrStorageLatched)

	built, err := New(options)
	require.Error(t, err)
	assert.Nil(t, built)
	assert.ErrorIs(t, err, authorization.ErrStorageUnavailable)
}

func createCompositionServer(t *testing.T, repository *servers.Repository, namespace string, enabled bool, executable string) servers.Server {
	t.Helper()
	digest := sha256.Sum256([]byte(namespace))
	result, err := repository.Create(context.Background(), servers.CreateRequest{
		Definition: servers.Definition{Namespace: namespace, DisplayName: namespace, Enabled: enabled, Transport: contract.StdioTransport{
			Kind: contract.TransportStdio, Executable: executable, Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{},
		}},
		Idempotency: &servers.IdempotencyRequest{AuthorityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Method: "POST", Route: "/api/v1/servers", Key: namespace, RequestHash: digest},
	})
	require.NoError(t, err)
	return result.Server
}

func newCompositionOptions(t *testing.T) (Options, func()) {
	t.Helper()
	return newCompositionOptionsWithFault(t, nil)
}

func newCompositionOptionsWithFault(t *testing.T, fault func(storage.FaultPoint) error) (Options, func()) {
	t.Helper()
	dataDir := t.TempDir()
	require.NoError(t, os.Chmod(dataDir, 0o700))
	ownership, err := gatewaypaths.Acquire(dataDir)
	require.NoError(t, err)
	store, err := storage.InitializeWithFaultInjection(context.Background(), ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV", fault)
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
			Ready:          func() bool { return true },
		}, func() {
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
		}
}
