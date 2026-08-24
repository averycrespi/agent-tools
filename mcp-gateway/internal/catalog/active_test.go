package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const activeProcessID = "01ARZ3NDEKTSV4RRFFQ69G5FAY"

func TestActiveRegistryPublishesDurableBeforeOneImmutableGeneration(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	initial := registry.Summary()
	assert.Equal(t, activeProcessID+"-0", initial.ActiveGeneration)
	assert.Equal(t, contract.AggregateCatalogEmpty, initial.ActiveState)
	assert.Nil(t, initial.ChangedAt)

	observedDurable := false
	status, err := registry.Publish(context.Background(), Publication{
		Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1,
		Candidate: candidateFor(t, server.ID, "sample", "one", "two"),
		Current: func() bool {
			durable, statusErr := repository.Status(context.Background(), server.ID)
			observedDurable = statusErr == nil && durable.Revision != nil && *durable.Revision == "1"
			return true
		},
	})
	require.NoError(t, err)
	assert.True(t, observedDurable)
	assert.Equal(t, contract.ActiveCatalogCurrent, status.State)
	assert.Equal(t, "1", *status.Revision)
	assert.Equal(t, int64(2), status.ToolCount)
	assert.Equal(t, activeProcessID+"-1", registry.Summary().ActiveGeneration)
	page, err := registry.List(nil, 100)
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, []string{"one", "two"}, descriptorNames(page.Items))

	page.Items[0].Resource.UpstreamName = "mutated"
	again, err := registry.List(nil, 100)
	require.NoError(t, err)
	assert.NotEqual(t, "mutated", again.Items[0].Resource.UpstreamName)

	restarted, err := NewActiveRegistry(repository, clock, "01ARZ3NDEKTSV4RRFFQ69G5FAZ")
	require.NoError(t, err)
	assert.Equal(t, contract.ActiveCatalogAbsent, restarted.Status(server.ID).State)
	assert.Equal(t, contract.AggregateCatalogEmpty, restarted.Summary().ActiveState)
	assert.Equal(t, "01ARZ3NDEKTSV4RRFFQ69G5FAZ-0", restarted.Summary().ActiveGeneration)
}

func TestActiveRegistryCommitThenStaleRuntimePublishesNothing(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	currentChecks := 0
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool {
		currentChecks++
		return currentChecks == 1
	}})
	assert.ErrorIs(t, err, servers.ErrStaleRevision)
	durable, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", *durable.Revision)
	active := registry.Status(server.ID)
	assert.Equal(t, contract.ActiveCatalogAbsent, active.State)
	assert.Nil(t, active.Revision)
	assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
}

func TestActiveRegistryPerServerCapacityRejectsBeforeDurableMutation(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	names := make([]string, fixedLimit("active_tools_per_server")+1)
	for index := range names {
		names[index] = fmt.Sprintf("tool_%d", index)
	}
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", names...), Current: func() bool { return true }})
	assert.ErrorIs(t, err, servers.ErrResourceLimit)
	durable, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	assert.Nil(t, durable.Revision)
	assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
}

func TestActiveRegistryCapacityReservationIsAtomicAcrossServers(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	serverA := createCatalogServer(t, serverRepository, "a")
	serverB := createCatalogServer(t, serverRepository, "b")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	registry.servers["occupied"] = activeServerSnapshot{State: contract.ActiveCatalogCurrent, Tools: make([]ActiveTool, fixedLimit("active_tools")-1)}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, publication := range []Publication{
		{Fence: catalogFence(serverA.ID, "0"), RuntimeID: "runtime-a", RuntimeGeneration: 1, Candidate: candidateFor(t, serverA.ID, "a", "one"), Current: func() bool { return true }},
		{Fence: catalogFence(serverB.ID, "0"), RuntimeID: "runtime-b", RuntimeGeneration: 1, Candidate: candidateFor(t, serverB.ID, "b", "one"), Current: func() bool { return true }},
	} {
		wait.Add(1)
		go func(candidate Publication) {
			defer wait.Done()
			<-start
			_, publishErr := registry.Publish(context.Background(), candidate)
			results <- publishErr
		}(publication)
	}
	close(start)
	wait.Wait()
	close(results)
	successes, limited := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if assert.ErrorIs(t, result, servers.ErrResourceLimit) {
			limited++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, limited)
	statusA, err := repository.Status(context.Background(), serverA.ID)
	require.NoError(t, err)
	statusB, err := repository.Status(context.Background(), serverB.ID)
	require.NoError(t, err)
	commits := 0
	if statusA.Revision != nil {
		commits++
	}
	if statusB.Revision != nil {
		commits++
	}
	assert.Equal(t, 1, commits)
}

func TestActiveRegistryPublishesCurrentRouteWithPinnedHeaderBindingsAndInvalidatesOldCapability(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtime, transport := newRouteRuntime(t)
	raw := RawTool{UpstreamName: "one", ExternalName: "sample.one", Descriptor: json.RawMessage(`{"name":"one","inputSchema":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"X-Region"}}}}`)}
	normalized, err := NormalizeTool(raw, NormalizeOptions{ServerID: server.ID, AllowHeaderBindings: true})
	require.NoError(t, err)
	status, err := registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: NormalizedCandidate{Tools: []NormalizedTool{normalized}, RawCount: 1, Pages: 1}, Current: func() bool { return true }, Runtime: runtime})
	require.NoError(t, err)
	require.NotNil(t, status.Revision)
	page, err := registry.List(nil, 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	capability, ok := registry.Routes().Resolve(page.Items[0].Resource.ID)
	require.True(t, ok)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	result := lease.Execute(context.Background(), json.RawMessage(`{"region":"us-west1"}`))
	require.NoError(t, result.Err)
	assert.Equal(t, map[string]string{"X-Region": "us-west1"}, transport.last().ParameterHeaders)

	lease, err = capability.Acquire(context.Background())
	require.NoError(t, err)
	assert.True(t, registry.MarkStale(server.ID, "runtime-1", 1))
	result = lease.Execute(context.Background(), json.RawMessage(`{"region":"us-east1"}`))
	assert.Equal(t, downstream.FailurePreStart, result.Failure)
	var rejection *downstream.PreStartRejection
	require.ErrorAs(t, result.Err, &rejection)
	assert.Equal(t, downstream.RejectionStale, rejection.Reason)
	_, err = capability.Acquire(context.Background())
	require.ErrorAs(t, err, &rejection)
	assert.True(t, registry.Withdraw(server.ID, "runtime-1", contract.ActiveCatalogUnavailable))
	_, ok = registry.Routes().Resolve(page.Items[0].Resource.ID)
	assert.False(t, ok)
	_, err = capability.Acquire(context.Background())
	require.ErrorAs(t, err, &rejection)
	assert.Equal(t, downstream.RejectionWithdrawn, rejection.Reason)
}

type routeTransport struct {
	mu       sync.Mutex
	messages []downstream.Message
}

func (transport *routeTransport) Kind() downstream.TransportKind { return downstream.TransportHTTP }
func (transport *routeTransport) Exchange(_ context.Context, message downstream.Message) (downstream.WireResponse, error) {
	if message.MarkHandoff != nil {
		message.MarkHandoff()
	}
	transport.mu.Lock()
	transport.messages = append(transport.messages, message)
	transport.mu.Unlock()
	var request struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(message.Payload, &request)
	member := `"result":{"content":[],"isError":false}`
	if message.Method == "server/discover" {
		member = `"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, request.ID, member)
	return downstream.WireResponse{StatusCode: 200, ContentType: "application/json", Body: []byte(body)}, nil
}
func (transport *routeTransport) Notify(context.Context, downstream.Message) (downstream.WireResponse, error) {
	return downstream.WireResponse{StatusCode: 202, ContentType: "application/json"}, nil
}
func (transport *routeTransport) Close(context.Context) error { return nil }
func (transport *routeTransport) last() downstream.Message {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.messages[len(transport.messages)-1]
}

func newRouteRuntime(t *testing.T) (*downstream.Runtime, *routeTransport) {
	t.Helper()
	transport := new(routeTransport)
	negotiator, err := downstream.NewNegotiator(func(context.Context) (*downstream.Coordinator, error) {
		return downstream.NewCoordinator(transport)
	})
	require.NoError(t, err)
	runtime, err := negotiator.Negotiate(context.Background(), downstream.ModeModern)
	require.NoError(t, err)
	return runtime, transport
}

func TestActiveRegistryStaleRetentionWithdrawalAndCursorGeneration(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one", "two", "three"), Current: func() bool { return true }})
	require.NoError(t, err)
	first, err := registry.List(nil, 2)
	require.NoError(t, err)
	require.NotNil(t, first.Next)

	clock.now = clock.now.Add(time.Minute)
	assert.True(t, registry.MarkStale(server.ID, "runtime-1", 2))
	status := registry.Status(server.ID)
	assert.Equal(t, contract.ActiveCatalogStale, status.State)
	assert.Equal(t, int64(3), status.ToolCount)
	assert.Equal(t, int64(2), registry.Summary().IssueCount)
	assert.Equal(t, contract.AggregateCatalogDegraded, registry.Summary().ActiveState)
	_, err = registry.List(first.Next, 2)
	assert.ErrorIs(t, err, servers.ErrStaleCursor)

	assert.False(t, registry.Withdraw(server.ID, "older-runtime", contract.ActiveCatalogUnavailable))
	assert.True(t, registry.Withdraw(server.ID, "runtime-1", contract.ActiveCatalogUnavailable))
	status = registry.Status(server.ID)
	assert.Equal(t, contract.ActiveCatalogUnavailable, status.State)
	assert.Nil(t, status.Revision)
	assert.Zero(t, status.ToolCount)
	assert.Equal(t, contract.AggregateCatalogEmpty, registry.Summary().ActiveState)
}
