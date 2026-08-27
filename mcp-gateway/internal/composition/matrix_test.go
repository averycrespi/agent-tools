package composition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryBackend struct {
	mu     sync.Mutex
	values map[string]string
	reads  map[string]int
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{values: make(map[string]string), reads: make(map[string]int)}
}
func (*memoryBackend) Probe(context.Context, string) error { return nil }
func (backend *memoryBackend) Set(service, user, password string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.values[service+"\x00"+user] = password
	return nil
}
func (backend *memoryBackend) Get(service, user string) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.reads[user]++
	value, ok := backend.values[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (backend *memoryBackend) Delete(service, user string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	key := service + "\x00" + user
	if _, ok := backend.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(backend.values, key)
	return nil
}
func (backend *memoryBackend) readsFor(serverID string) int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	total := 0
	for item, count := range backend.reads {
		if strings.Contains(item, serverID) {
			total += count
		}
	}
	return total
}

type fixtureInput struct{ bytes.Buffer }

func (*fixtureInput) Close() error { return nil }

type fixtureStdio struct {
	frames chan []byte
	input  *fixtureInput
	mu     sync.Mutex
	stops  int
	stop   bool
}

func (runtime *fixtureStdio) Frames() <-chan []byte { return runtime.frames }
func (runtime *fixtureStdio) Input() io.WriteCloser { return runtime.input }
func (runtime *fixtureStdio) Stop(context.Context) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.stops++
	return runtime.stop
}

func (runtime *fixtureStdio) StopCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.stops
}

type controlledStopStdio struct {
	*fixtureStdio
	started chan struct{}
	release chan struct{}
	result  bool
	once    sync.Once
}

func (runtime *controlledStopStdio) Stop(context.Context) bool {
	runtime.once.Do(func() { close(runtime.started) })
	<-runtime.release
	runtime.mu.Lock()
	runtime.stops++
	runtime.mu.Unlock()
	return runtime.result
}

type compositionTransport struct{ delegate downstream.Transport }

func (transport *compositionTransport) Kind() downstream.TransportKind {
	return transport.delegate.Kind()
}
func (transport *compositionTransport) Exchange(_ context.Context, message downstream.Message) (downstream.WireResponse, error) {
	member := `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`
	switch message.Method {
	case "initialize":
		member = `{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}`
	case "tools/list":
		member = `{"tools":[{"name":"one","description":"fixture","inputSchema":{"type":"object"}}]}`
	}
	var request struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(message.Payload, &request); err != nil {
		return downstream.WireResponse{}, err
	}
	return downstream.WireResponse{Body: []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, request.ID, member))}, nil
}
func (*compositionTransport) Notify(context.Context, downstream.Message) (downstream.WireResponse, error) {
	return downstream.WireResponse{}, nil
}
func (transport *compositionTransport) Close(ctx context.Context) error {
	return transport.delegate.Close(ctx)
}

func TestProductionCompositionAuthorityMatrixUsesOneGraphAndActualOwners(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	var invalidationMu sync.Mutex
	var invalidations []contract.Invalidation
	options.Invalidate = func(invalidation contract.Invalidation) {
		invalidationMu.Lock()
		invalidations = append(invalidations, invalidation)
		invalidationMu.Unlock()
	}
	var stdioMu sync.Mutex
	var stdioDefinitions []runtimes.StdioDefinition
	hooks := constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(_ context.Context, definition runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			cloned := definition
			cloned.Secrets = make(map[string]string, len(definition.Secrets))
			for key, value := range definition.Secrets {
				cloned.Secrets[key] = value
			}
			stdioMu.Lock()
			stdioDefinitions = append(stdioDefinitions, cloned)
			stdioMu.Unlock()
			return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	}
	built, err := newWithHooks(options, hooks)
	require.NoError(t, err)
	defer built.shutdownConstructed()

	credentialFree := createServerWithTransport(t, built.servers, "free", contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
	static := createServerWithTransport(t, built.servers, "static", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token"}})
	bearer := createServerWithTransport(t, built.servers, "bearer", contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://bearer.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}})
	public := createOAuthCompositionServer(t, built.servers, "public", contract.TokenEndpointAuthNone)
	confidential := createOAuthCompositionServer(t, built.servers, "confidential", contract.TokenEndpointAuthClientSecretBasic)

	publishStaticCompositionCredential(t, built, static, []string{"token"}, map[string]string{"token": "static-canary"})
	publishStaticCompositionCredential(t, built, bearer, []string{"bearer"}, map[string]string{"bearer": "bearer-canary"})
	publishOAuthCompositionAuthority(t, built, options, public, contract.TokenEndpointAuthNone, "", "public-token-canary")
	publishOAuthCompositionAuthority(t, built, options, confidential, contract.TokenEndpointAuthClientSecretBasic, "client-canary", "confidential-token-canary")
	credentialFree = enableCompositionServer(t, built.servers, credentialFree)
	static = enableCompositionServer(t, built.servers, static)
	bearer = enableCompositionServer(t, built.servers, bearer)
	public = enableCompositionServer(t, built.servers, public)
	confidential = enableCompositionServer(t, built.servers, confidential)
	serversByKind := []servers.Server{credentialFree, static, bearer, public, confidential}
	readsBeforeStart := make(map[string]int, len(serversByKind))
	for _, server := range serversByKind {
		readsBeforeStart[server.ID] = backend.readsFor(server.ID)
	}

	require.NoError(t, built.Start(context.Background()))
	for _, server := range serversByKind {
		server := server
		require.Eventually(t, func() bool {
			status := built.RuntimeStatus(server.ID)
			return status.State != contract.RuntimeInactive && status.State != contract.RuntimeActivating && status.Reconciliation.InUse == 0
		}, 2*time.Second, time.Millisecond, server.Namespace)
		if built.RuntimeStatus(server.ID).State != contract.RuntimeActive {
			built.manager.Trigger(server.ID, nil, true)
			require.Eventually(t, func() bool {
				status := built.RuntimeStatus(server.ID)
				return status.State == contract.RuntimeActive && status.Reconciliation.InUse == 0
			}, 2*time.Second, time.Millisecond, server.Namespace)
		}
	}
	for _, server := range serversByKind {
		if built.RuntimeStatus(server.ID).CatalogState != contract.ActiveCatalogCurrent {
			var operation servers.OperationResult
			require.Eventually(t, func() bool {
				var createErr error
				operation, createErr = built.servers.CreateOperation(context.Background(), servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: server.DesiredRevision})
				return createErr == nil
			}, 2*time.Second, time.Millisecond)
			built.manager.Trigger(server.ID, &operation.Operation.ID, false)
		}
		require.Eventually(t, func() bool {
			return built.RuntimeStatus(server.ID).CatalogState == contract.ActiveCatalogCurrent
		}, 2*time.Second, time.Millisecond, server.Namespace)
		assert.Equal(t, int64(0), built.CatalogServerStatus(server.ID).InUse)
		assert.Equal(t, int64(0), built.DispatchServerStatus(server.ID).InUse)
	}
	assert.Equal(t, int64(5), built.RuntimeOccupancy().InUse)
	assert.Equal(t, int64(0), built.CatalogTraversalStatus().InUse)
	assert.Equal(t, int64(0), built.DispatchStatus().InUse)
	assert.Equal(t, int64(5), built.ActiveCatalog().Occupancy().InUse)
	page, err := built.ActiveCatalog().List(nil, 100)
	require.NoError(t, err)
	require.Len(t, page.Items, 5)
	for _, descriptor := range page.Items {
		_, ok := built.ActiveCatalog().Routes().Resolve(descriptor.Resource.ID)
		assert.True(t, ok)
	}
	assert.Equal(t, readsBeforeStart[credentialFree.ID], backend.readsFor(credentialFree.ID))
	for _, server := range []servers.Server{static, bearer, public, confidential} {
		assert.Greater(t, backend.readsFor(server.ID), readsBeforeStart[server.ID], server.Namespace)
	}
	stdioMu.Lock()
	require.NotEmpty(t, stdioDefinitions)
	for _, definition := range stdioDefinitions {
		assert.Equal(t, map[string]string{"token": "static-canary"}, definition.Secrets)
	}
	stdioMu.Unlock()

	invalidationMu.Lock()
	safeInvalidations := append([]contract.Invalidation(nil), invalidations...)
	invalidationMu.Unlock()
	safe, err := json.Marshal(struct {
		Summary       contract.CatalogSummary
		Items         []contract.ToolDescriptor
		Status        []runtimes.Status
		Invalidations []contract.Invalidation
	}{Summary: built.ActiveCatalog().Summary(), Items: descriptorResources(page.Items), Status: compositionStatuses(built, serversByKind), Invalidations: safeInvalidations})
	require.NoError(t, err)
	for _, canary := range []string{"static-canary", "bearer-canary", "public-token-canary", "client-canary", "confidential-token-canary"} {
		assert.NotContains(t, string(safe), canary)
	}
	agentIngress, ok := built.AgentIngress()
	require.True(t, ok)
	ingress := mcpingress.New(mcpingress.Options{Authenticator: agentIngress.Authenticator, ListTools: agentIngress.ListTools})
	defer ingress.Shutdown()
	request := httptest.NewRequest("POST", "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	_, err = ingress.Authenticate(context.Background(), request, contract.AuthorityAgent)
	assert.Error(t, err)

	result := <-built.Drain(context.Background())
	assert.Equal(t, 5, result.Verified)
	assert.Zero(t, result.Unconfirmed)
}

func TestProductionCompositionDrainWaitsForConstructingCleanupAndIsIdempotent(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	runtime := &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			started <- struct{}{}
			<-release
			return runtime, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createServerWithTransport(t, built.servers, "drain-constructing", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
	enableCompositionServer(t, built.servers, server)
	require.NoError(t, built.Start(context.Background()))
	<-started

	first := built.Drain(context.Background())
	second := built.Drain(context.Background())
	select {
	case <-first:
		t.Fatal("drain completed before constructing runtime returned")
	default:
	}
	close(release)
	assert.Equal(t, runtimes.DrainResult{Verified: 1}, <-first)
	assert.Equal(t, runtimes.DrainResult{Verified: 1}, <-second)
	assert.Equal(t, 1, runtime.StopCount())
	assert.Zero(t, built.RuntimeOccupancy().InUse)
	assert.False(t, built.callbacks.running())
	assert.Equal(t, contract.ActiveCatalogAbsent, built.ActiveCatalog().Status(server.ID).State)
}

func TestProductionCompositionDrainDeadlineFencesLateConstructingCompletion(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	runtime := &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}
	var invalidations atomic.Int64
	options.Invalidate = func(contract.Invalidation) { invalidations.Add(1) }
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			started <- struct{}{}
			<-release
			return runtime, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createServerWithTransport(t, built.servers, "drain-deadline", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
	enableCompositionServer(t, built.servers, server)
	require.NoError(t, built.Start(context.Background()))
	<-started
	releasePipeline, entered := built.invocationPipelines.TryEnter()
	require.True(t, entered)
	defer releasePipeline()

	ctx, cancel := context.WithCancel(context.Background())
	done := built.Drain(ctx)
	cancel()
	assert.Equal(t, runtimes.DrainResult{Unconfirmed: 1}, <-done)
	_, entered = built.invocationPipelines.TryEnter()
	assert.False(t, entered, "deadline-expired drain reopened invocation admission")
	releasePipeline()
	afterDrain := invalidations.Load()
	close(release)
	require.True(t, built.manager.Wait(context.Background()))
	assert.Equal(t, afterDrain, invalidations.Load())
	assert.Equal(t, 1, runtime.StopCount())
	assert.Zero(t, built.RuntimeOccupancy().InUse)
	assert.Equal(t, contract.ActiveCatalogAbsent, built.ActiveCatalog().Status(server.ID).State)
}

func TestProductionCompositionReplacementWithdrawsBeforeStopAndConstructsOnlyAfterProof(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopResult bool
	}{
		{name: "verified stop admits replacement", stopResult: true},
		{name: "unconfirmed stop blocks replacement", stopResult: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			backend := newMemoryBackend()
			stopStarted, stopRelease := make(chan struct{}), make(chan struct{})
			replacementStarted, replacementRelease := make(chan struct{}), make(chan struct{})
			oldRuntime := &controlledStopStdio{fixtureStdio: &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput)}, started: stopStarted, release: stopRelease, result: test.stopResult}
			var starts atomic.Int64
			built, err := newWithHooks(options, constructorHooks{
				provider: func(installationID string) (*keyring.Provider, error) {
					return keyring.NewProviderWithBackend(installationID, backend)
				},
				startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
					if starts.Add(1) == 1 {
						return oldRuntime, nil
					}
					close(replacementStarted)
					<-replacementRelease
					return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}, nil
				},
				newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
					return downstream.NewCoordinator(&compositionTransport{delegate: transport})
				},
			})
			require.NoError(t, err)
			defer built.shutdownConstructed()
			namespace := "replace-blocked"
			if test.stopResult {
				namespace = "replace-verified"
			}
			server := createServerWithTransport(t, built.servers, namespace, contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
			server = enableCompositionServer(t, built.servers, server)
			require.NoError(t, built.Start(context.Background()))
			require.Eventually(t, func() bool { return built.RuntimeStatus(server.ID).CatalogState == contract.ActiveCatalogCurrent }, 2*time.Second, time.Millisecond)
			page, err := built.ActiveCatalog().List(nil, 10)
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			resourceID := page.Items[0].Resource.ID
			_, routePresent := built.ActiveCatalog().Routes().Resolve(resourceID)
			require.True(t, routePresent)

			operation, err := built.servers.CreateOperation(context.Background(), servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationReload, ExpectedDesiredRevision: server.DesiredRevision})
			require.NoError(t, err)
			built.manager.Trigger(server.ID, &operation.Operation.ID, true)
			<-stopStarted
			_, routePresent = built.ActiveCatalog().Routes().Resolve(resourceID)
			assert.False(t, routePresent)
			assert.Equal(t, contract.ActiveCatalogUnavailable, built.ActiveCatalog().Status(server.ID).State)
			current, err := built.servers.GetOperation(context.Background(), operation.Operation.ID)
			require.NoError(t, err)
			assert.Equal(t, contract.OperationRunning, current.State)
			assert.Equal(t, int64(1), starts.Load())
			close(stopRelease)

			if test.stopResult {
				<-replacementStarted
				_, routePresent = built.ActiveCatalog().Routes().Resolve(resourceID)
				assert.False(t, routePresent)
				current, err = built.servers.GetOperation(context.Background(), operation.Operation.ID)
				require.NoError(t, err)
				assert.Equal(t, contract.OperationRunning, current.State)
				close(replacementRelease)
				require.Eventually(t, func() bool {
					current, err = built.servers.GetOperation(context.Background(), operation.Operation.ID)
					return err == nil && current.State == contract.OperationSucceeded && built.RuntimeStatus(server.ID).CatalogState == contract.ActiveCatalogCurrent
				}, 2*time.Second, time.Millisecond)
				_, routePresent = built.ActiveCatalog().Routes().Resolve(resourceID)
				assert.True(t, routePresent)
				assert.Equal(t, int64(2), starts.Load())
			} else {
				close(replacementRelease)
				require.Eventually(t, func() bool {
					current, err = built.servers.GetOperation(context.Background(), operation.Operation.ID)
					return err == nil && current.State == contract.OperationFailed && current.Reason != nil && *current.Reason == contract.ReasonStopUnconfirmed
				}, 2*time.Second, time.Millisecond)
				assert.Equal(t, int64(1), starts.Load())
				assert.Equal(t, int64(1), built.RuntimeOccupancy().InUse)
				assert.Equal(t, int64(0), built.RuntimeStatus(server.ID).Reconciliation.InUse)
				_, routePresent = built.ActiveCatalog().Routes().Resolve(resourceID)
				assert.False(t, routePresent)
			}
		})
	}
}

func TestProductionCompositionDrainWithdrawsBeforeBlockedStopAndStopsOnce(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	stopStarted, stopRelease := make(chan struct{}), make(chan struct{})
	runtime := &controlledStopStdio{fixtureStdio: &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput)}, started: stopStarted, release: stopRelease}
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) { return runtime, nil },
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createServerWithTransport(t, built.servers, "drain-blocked", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
	enableCompositionServer(t, built.servers, server)
	require.NoError(t, built.Start(context.Background()))
	require.Eventually(t, func() bool { return built.RuntimeStatus(server.ID).CatalogState == contract.ActiveCatalogCurrent }, 2*time.Second, time.Millisecond)
	page, err := built.ActiveCatalog().List(nil, 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	resourceID := page.Items[0].Resource.ID
	beforeGeneration := page.Summary.ActiveGeneration
	_, routePresent := built.ActiveCatalog().Routes().Resolve(resourceID)
	require.True(t, routePresent)

	first := built.Drain(context.Background())
	<-stopStarted
	_, routePresent = built.ActiveCatalog().Routes().Resolve(resourceID)
	assert.False(t, routePresent)
	assert.NotEqual(t, beforeGeneration, built.ActiveCatalog().Summary().ActiveGeneration)
	assert.Equal(t, contract.ActiveCatalogUnavailable, built.ActiveCatalog().Status(server.ID).State)
	close(stopRelease)
	assert.Equal(t, runtimes.DrainResult{Unconfirmed: 1}, <-first)
	assert.Equal(t, runtimes.DrainResult{Unconfirmed: 1}, <-built.Drain(context.Background()))
	assert.Equal(t, 1, runtime.StopCount())
	assert.Equal(t, int64(1), built.RuntimeOccupancy().InUse)
}

func TestProductionCompositionReportsConstructingAndRetainedBlockedStopFromActualOwner(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var starts atomic.Int64
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			starts.Add(1)
			started <- struct{}{}
			<-release
			return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: false}, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createServerWithTransport(t, built.servers, "blocked", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
	server = enableCompositionServer(t, built.servers, server)

	require.NoError(t, built.Start(context.Background()))
	<-started
	assert.Equal(t, int64(1), built.RuntimeOccupancy().InUse)
	assert.Equal(t, contract.RuntimeActivating, built.RuntimeStatus(server.ID).State)
	built.manager.Trigger(server.ID, nil, false)
	close(release)
	require.Eventually(t, func() bool {
		status := built.RuntimeStatus(server.ID)
		return status.State == contract.RuntimeDegraded && status.Reason != nil && *status.Reason == contract.ReasonStopUnconfirmed
	}, 2*time.Second, time.Millisecond)
	assert.Equal(t, int64(1), built.RuntimeOccupancy().InUse)
	assert.Equal(t, int64(1), starts.Load())
}

func TestProductionCompositionFinalizesStaticDisconnectCatalogLifecycle(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	server := createServerWithTransport(t, built.servers, "disconnect-catalog", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token"}})
	publishStaticCompositionCredential(t, built, server, []string{"token"}, map[string]string{"token": "canary"})
	server = enableCompositionServer(t, built.servers, server)
	require.NoError(t, built.Start(context.Background()))
	require.Eventually(t, func() bool { return built.RuntimeStatus(server.ID).CatalogState == contract.ActiveCatalogCurrent }, 2*time.Second, time.Millisecond)
	operation, err := built.servers.CreateOperation(context.Background(), servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationDisconnectCredentials, ExpectedDesiredRevision: server.DesiredRevision})
	require.NoError(t, err)
	built.manager.Trigger(server.ID, &operation.Operation.ID, false)
	require.Eventually(t, func() bool {
		current, getErr := built.servers.GetOperation(context.Background(), operation.Operation.ID)
		return getErr == nil && (current.State == contract.OperationSucceeded || current.State == contract.OperationFailed)
	}, 2*time.Second, time.Millisecond)
	current, err := built.servers.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationSucceeded, current.State)
	status, err := built.catalogRepository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.DurableCatalogUnavailable, status.State)
	assert.Equal(t, contract.ActiveCatalogUnavailable, built.activeCatalog.Status(server.ID).State)
}

func TestProductionCompositionRejectsMissingExtraAndStaleStaticAuthorityBeforeConstruction(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	backend := newMemoryBackend()
	var starts atomic.Int64
	built, err := newWithHooks(options, constructorHooks{
		provider: func(installationID string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(installationID, backend)
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			starts.Add(1)
			return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}, nil
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	transport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token"}}
	missing := createServerWithTransport(t, built.servers, "missing", transport)
	extra := createServerWithTransport(t, built.servers, "extra", transport)
	stale := createServerWithTransport(t, built.servers, "stale", transport)
	publishStaticCompositionCredential(t, built, extra, []string{"token"}, map[string]string{"token": "extra-canary", "unexpected": "extra-canary"})
	publishStaticCompositionCredential(t, built, stale, []string{"token"}, map[string]string{"token": "stale-old-canary"})
	staleNamespace, err := keyring.NewNamespace(options.InstallationID, stale.ID, keyring.RecordStaticCredential)
	require.NoError(t, err)
	newer, err := servercredentials.EncodeStaticGeneration(map[string]string{"token": "stale-new-canary"})
	require.NoError(t, err)
	_, err = built.keyring.Replace(context.Background(), staleNamespace, newer)
	require.NoError(t, err)
	missing = enableCompositionServer(t, built.servers, missing)
	extra = enableCompositionServer(t, built.servers, extra)
	stale = enableCompositionServer(t, built.servers, stale)

	require.NoError(t, built.Start(context.Background()))
	for _, server := range []servers.Server{missing, extra, stale} {
		server := server
		require.Eventually(t, func() bool {
			status := built.RuntimeStatus(server.ID)
			return status.State != contract.RuntimeInactive && status.Reconciliation.InUse == 0
		}, 2*time.Second, time.Millisecond)
		status := built.RuntimeStatus(server.ID)
		assert.NotEqual(t, contract.RuntimeActive, status.State)
		assert.Nil(t, status.RuntimeID)
	}
	assert.Zero(t, starts.Load())
	assert.Zero(t, built.RuntimeOccupancy().InUse)
	assert.Zero(t, backend.readsFor(missing.ID))
	assert.Positive(t, backend.readsFor(extra.ID))
	assert.Positive(t, backend.readsFor(stale.ID))
	safe, err := json.Marshal(compositionStatuses(built, []servers.Server{missing, extra, stale}))
	require.NoError(t, err)
	for _, canary := range []string{"extra-canary", "stale-old-canary", "stale-new-canary"} {
		assert.NotContains(t, string(safe), canary)
	}
}

func createServerWithTransport(t *testing.T, repository *servers.Repository, namespace string, transport contract.Transport) servers.Server {
	t.Helper()
	result, err := repository.Create(context.Background(), servers.CreateRequest{Definition: servers.Definition{Namespace: namespace, DisplayName: namespace, Enabled: false, Transport: transport}, Idempotency: compositionIdempotency(namespace)})
	require.NoError(t, err)
	return result.Server
}

func enableCompositionServer(t *testing.T, repository *servers.Repository, server servers.Server) servers.Server {
	t.Helper()
	enabled := true
	result, err := repository.Patch(context.Background(), server.ID, server.DesiredRevision, servers.Patch{Enabled: &enabled})
	require.NoError(t, err)
	return result.Server
}

func createOAuthCompositionServer(t *testing.T, repository *servers.Repository, namespace string, method contract.TokenEndpointAuthMethod) servers.Server {
	t.Helper()
	issuer := "https://issuer.example"
	return createServerWithTransport(t, repository, namespace, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://" + namespace + ".example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &issuer, ClientID: namespace + "-client", TokenEndpointAuthMethod: method}, TrustedOrigins: []string{}, RequestOfflineAccess: false}})
}

func compositionIdempotency(key string) *servers.IdempotencyRequest {
	digest := sha256.Sum256([]byte(key))
	return &servers.IdempotencyRequest{AuthorityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Method: "POST", Route: "/api/v1/servers", Key: key, RequestHash: digest}
}

func publishStaticCompositionCredential(t *testing.T, built *Composition, server servers.Server, slots []string, values map[string]string) {
	t.Helper()
	plan, err := built.replacements.Prepare(context.Background(), servers.CredentialReplacementRequest{ServerID: server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: server.DesiredRevision, ExpectedCredentialRevision: "0", Slots: slots})
	require.NoError(t, err)
	secret, err := servercredentials.EncodeStaticGeneration(values)
	require.NoError(t, err)
	_, err = built.replacements.Replace(context.Background(), plan, secret)
	require.NoError(t, err)
}

func publishOAuthCompositionAuthority(t *testing.T, built *Composition, options Options, server servers.Server, method contract.TokenEndpointAuthMethod, clientSecret, accessToken string) {
	t.Helper()
	clientRevision := "0"
	if clientSecret != "" {
		plan, err := built.replacements.Prepare(context.Background(), servers.CredentialReplacementRequest{ServerID: server.ID, Kind: contract.ServerCredentialOAuthClient, ExpectedDesiredRevision: server.DesiredRevision, ExpectedCredentialRevision: "0"})
		require.NoError(t, err)
		_, err = built.replacements.Replace(context.Background(), plan, []byte(clientSecret))
		require.NoError(t, err)
		clientRevision = "1"
	}
	flow, err := built.servers.CreateAuthFlow(context.Background(), servers.AuthFlowCreateRequest{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision})
	require.NoError(t, err)
	resource := "https://" + server.Namespace + ".example/mcp"
	registration, err := built.servers.PublishPublicRegistration(context.Background(), servers.RegistrationFence{ServerID: server.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: clientRevision, ExpectedAuthFlowID: flow.Flow.ID}, servers.OAuthRegistrationAuthority{Revision: "0", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: server.Namespace + "-client", CallbackURL: options.CallbackURL, ResourceURL: resource, TokenEndpointAuthMethod: method, CreatedAt: options.Clock.Now().Format(time.RFC3339Nano)})
	require.NoError(t, err)
	_, err = built.servers.MarkAuthFlowAwaiting(context.Background(), flow.Flow.ID, server.DesiredRevision, registration.Revision)
	require.NoError(t, err)
	fence := servers.OAuthTokenFence{ServerID: server.ID, FlowID: flow.Flow.ID, ExpectedDesiredRevision: server.DesiredRevision, ExpectedRegistrationRevision: registration.Revision, ExpectedOAuthClientRevision: clientRevision, ExpectedOAuthTokensRevision: "0"}
	_, err = built.servers.BeginAuthFlowExchange(context.Background(), fence)
	require.NoError(t, err)
	callback, err := built.servers.OAuthTokenAuthorityCallback(fence)
	require.NoError(t, err)
	namespace, err := keyring.NewNamespace(options.InstallationID, server.ID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	generation, err := json.Marshal(oauth.TokenGeneration{Version: 1, ServerID: server.ID, Issuer: "https://issuer.example", RegistrationRevision: registration.Revision, Resource: resource, AccessToken: accessToken, Scopes: []string{"read"}, ScopeSpecified: true, IssuedAt: options.Clock.Now().Format(time.RFC3339Nano)})
	require.NoError(t, err)
	_, err = built.keyring.ReplaceFencedAfterAuthorizationSuccess(context.Background(), namespace, generation, callback)
	require.NoError(t, err)
}

func descriptorResources(records []catalog.DescriptorRecord) []contract.ToolDescriptor {
	resources := make([]contract.ToolDescriptor, len(records))
	for index := range records {
		resources[index] = records[index].Resource
	}
	return resources
}

func compositionStatuses(built *Composition, values []servers.Server) []runtimes.Status {
	statuses := make([]runtimes.Status, len(values))
	for index := range values {
		statuses[index] = built.RuntimeStatus(values[index].ID)
	}
	return statuses
}
