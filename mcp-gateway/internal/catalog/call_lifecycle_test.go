package catalog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinnedDirectCallRejectsWithdrawalBetweenResolutionAcquireAndExecute(t *testing.T) {
	t.Run("withdraw after resolution", func(t *testing.T) {
		registry, serverID, target, _ := publishLifecycleCallTarget(t)

		require.True(t, registry.WithdrawExact(serverID, "runtime-1", 1, contract.ActiveCatalogUnavailable))
		_, found := registry.Routes().ResolveCall("sample.echo")
		assert.False(t, found)
		_, err := target.Capability.Acquire(context.Background())
		var rejection *downstream.PreStartRejection
		require.ErrorAs(t, err, &rejection)
		assert.Equal(t, downstream.RejectionWithdrawn, rejection.Reason)
		assert.Zero(t, registry.Routes().Status().InUse)
		assert.Zero(t, registry.Routes().ServerStatus(serverID).InUse)
	})

	t.Run("withdraw after acquisition", func(t *testing.T) {
		registry, serverID, target, transport := publishLifecycleCallTarget(t)
		lease, err := target.Capability.Acquire(context.Background())
		require.NoError(t, err)

		require.True(t, registry.WithdrawExact(serverID, "runtime-1", 1, contract.ActiveCatalogUnavailable))
		result := lease.Execute(context.Background(), json.RawMessage(`{"value":1}`))

		assert.Equal(t, downstream.FailurePreStart, result.Failure)
		assert.Error(t, result.Err)
		assert.Zero(t, transport.toolCallCount())
		assert.Zero(t, registry.Routes().Status().InUse)
		assert.Zero(t, registry.Routes().ServerStatus(serverID).InUse)
	})
}

func TestPinnedDirectCallDrainAfterHandoffIsUncertainAndReleasesPermits(t *testing.T) {
	registry, serverID, target, transport := publishLifecycleCallTarget(t)
	transport.callStarted = make(chan struct{})
	lease, err := target.Capability.Acquire(context.Background())
	require.NoError(t, err)
	result := make(chan downstream.CallResult, 1)
	go func() { result <- lease.Execute(context.Background(), json.RawMessage(`{"value":1}`)) }()
	<-transport.callStarted

	registry.Drain()

	select {
	case call := <-result:
		assert.Equal(t, downstream.FailureStartUncertain, call.Failure)
		assert.ErrorIs(t, call.Err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("drain did not cancel the handed-off direct call")
	}
	assert.Equal(t, 1, transport.toolCallCount())
	assert.Zero(t, registry.Routes().Status().InUse)
	assert.Zero(t, registry.Routes().ServerStatus(serverID).InUse)
	_, found := registry.Routes().ResolveCall("sample.echo")
	assert.False(t, found)
}

func publishLifecycleCallTarget(t *testing.T) (*ActiveRegistry, string, CallTarget, *routeTransport) {
	t.Helper()
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtime, transport := newRouteRuntime(t)
	normalized := normalizeCallTool(t, server.ID, "sample.echo", `{"name":"echo","inputSchema":{"type":"object"}}`)
	_, err = registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: NormalizedCandidate{Tools: []NormalizedTool{normalized}, RawCount: 1, Pages: 1},
		Current:   func() bool { return true }, Runtime: runtime,
	})
	require.NoError(t, err)
	target, found := registry.Routes().ResolveCall("sample.echo")
	require.True(t, found)
	return registry, server.ID, target, transport
}

func (transport *routeTransport) toolCallCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	count := 0
	for _, message := range transport.messages {
		if message.Method == "tools/call" {
			count++
		}
	}
	return count
}
