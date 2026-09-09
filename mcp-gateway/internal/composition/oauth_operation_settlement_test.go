package composition

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/api"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/require"
)

func TestOAuthCallbackSettlesDurableOperationAndRestoresAdmission(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	var built *Composition
	var mu sync.Mutex
	var observed []servers.Operation
	options.Invalidate = func(event contract.Invalidation) {
		if built == nil || event.Kind != contract.InvalidationServerOperations || event.ResourceID == nil {
			return
		}
		operation, err := built.servers.GetOperation(context.Background(), *event.ResourceID)
		if err == nil {
			mu.Lock()
			observed = append(observed, operation)
			mu.Unlock()
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(release) })
	oldRuntime := &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}
	var starts atomic.Int64
	var err error
	built, err = newWithHooks(options, constructorHooks{
		provider: func(id string) (*keyring.Provider, error) {
			return keyring.NewProviderWithBackend(id, newMemoryBackend())
		},
		startStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			if starts.Add(1) == 1 {
				close(entered)
				<-release
				return oldRuntime, nil
			}
			return &fixtureStdio{frames: make(chan []byte), input: new(fixtureInput), stop: true}, nil
		},
		newCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
			return downstream.NewCoordinator(&compositionTransport{delegate: transport})
		},
	})
	require.NoError(t, err)
	defer built.shutdownConstructed()
	defer releaseFirst()
	server := createServerWithTransport(t, built.servers, "oauth-settlement", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
	server = enableCompositionServer(t, built.servers, server)
	require.NoError(t, built.Start(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("initial construction did not start")
	}
	page, err := built.servers.ListOperations(ctx, server.ID, nil, 100)
	require.NoError(t, err)
	var original servers.Operation
	for _, operation := range page.Items {
		if operation.State == contract.OperationRunning {
			original = operation
		}
	}
	require.NotEmpty(t, original.ID)
	state := built.OperationState(ctx, server.ID)
	request := servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationRefreshCatalog, ExpectedDesiredRevision: server.DesiredRevision, TriggerState: &state, Idempotency: compositionIdempotency("post-oauth-refresh")}
	_, err = built.servers.CreateOperation(ctx, request)
	require.ErrorIs(t, err, servers.ErrOperationConflict)
	callbacks := newOAuthCallbackListeners(built, options)
	callbacks.notify(ctx, oauth.CallbackResult{ServerID: server.ID, Outcome: oauth.CallbackSucceeded})
	releaseFirst()
	require.True(t, built.manager.Wait(ctx))
	require.Equal(t, int64(2), starts.Load())
	require.Equal(t, 1, oldRuntime.StopCount())
	require.Equal(t, contract.ActiveCatalogCurrent, built.RuntimeStatus(server.ID).CatalogState)
	require.Zero(t, built.ReconciliationStatus().InUse)
	settled, err := built.servers.GetOperation(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, contract.OperationSuperseded, settled.State)
	require.Equal(t, original.Cause, settled.Cause)

	// Exercise the real operation-detail handler; authentication remains owned by
	// the unchanged HTTP boundary tests, not this composition fixture.
	handler := api.New(api.Options{Servers: built.servers})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/servers/"+server.ID+"/operations/"+original.ID, nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var detail contract.ServerOperation
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Equal(t, contract.OperationSuperseded, detail.State)
	require.Equal(t, original.ID, detail.ID)
	mu.Lock()
	snapshots := append([]servers.Operation(nil), observed...)
	mu.Unlock()
	found := false
	for _, operation := range snapshots {
		if operation.ID == original.ID && operation.State == contract.OperationSuperseded {
			found = true
		}
	}
	require.True(t, found, "settlement invalidation did not expose authoritative state")
	state = built.OperationState(ctx, server.ID)
	admitted, err := built.servers.CreateOperation(ctx, request)
	require.NoError(t, err)
	built.TriggerServer(ctx, server.ID, &admitted.Operation.ID, true)
	// Explicit refresh is not reconciliation work; use its authoritative terminal
	// read followed by the manager lock to join mutation cleanup.
	require.Eventually(t, func() bool {
		operation, getErr := built.servers.GetOperation(ctx, admitted.Operation.ID)
		return getErr == nil && operation.State == contract.OperationSucceeded
	}, 5*time.Second, time.Millisecond)
	require.Equal(t, contract.RuntimeActive, built.RuntimeStatus(server.ID).State)
}
