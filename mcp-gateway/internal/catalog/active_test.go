package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/mcpingress"
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

func TestActiveRegistryDurableCapacityReservationPrecedesMutation(t *testing.T) {
	repository, serverRepository, clock, store := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := range fixedLimit("durable_tool_identities_per_server") {
			_, err := transaction.Exec(`INSERT INTO durable_tool_identities (id, server_id, upstream_name, external_name, first_seen_at) VALUES (?, ?, ?, ?, ?)`, fmt.Sprintf("00000000000000000000%06d", index), server.ID, fmt.Sprintf("existing_%03d", index), fmt.Sprintf("sample.existing_%03d", index), catalogTime.Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
		}
		return nil
	}))
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	mutationEntered := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- store.Mutate(context.Background(), func(*sql.Tx) error {
			close(mutationEntered)
			<-releaseMutation
			return nil
		})
	}()
	<-mutationEntered

	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "new"), Current: func() bool { return true }})

	assert.ErrorIs(t, err, servers.ErrResourceLimit)
	assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
	durable, statusErr := repository.Status(context.Background(), server.ID)
	require.NoError(t, statusErr)
	assert.Nil(t, durable.Revision)
	close(releaseMutation)
	require.NoError(t, <-mutationDone)
}

func TestActiveRegistryPostCommitCauseTableRetainsDurableOnlyEvidence(t *testing.T) {
	tests := []struct {
		name  string
		cause PublicationFailureCause
		arm   func(*ActiveRegistry)
	}{
		{name: "stale", cause: PublicationFailureStale, arm: func(*ActiveRegistry) {}},
		{name: "repository_read", cause: PublicationFailureStorage, arm: func(registry *ActiveRegistry) {
			registry.beforeDescriptorRead = func() error { return errors.New("read latch") }
		}},
		{name: "drain", cause: PublicationFailureDrain, arm: func(registry *ActiveRegistry) {
			registry.afterCommit = func() {
				done := make(chan struct{})
				go func() {
					registry.Drain()
					close(done)
				}()
				require.Eventually(t, registry.draining.Load, time.Second, time.Millisecond)
				t.Cleanup(func() { <-done })
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, serverRepository, clock, _ := newCatalogRepository(t)
			server := createCatalogServer(t, serverRepository, "sample")
			registry, err := NewActiveRegistry(repository, clock, activeProcessID)
			require.NoError(t, err)
			checks := 0
			test.arm(registry)
			_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool {
				checks++
				return test.cause != PublicationFailureStale || checks != 2
			}})
			var failure *PublicationFailure
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, PublicationPhaseDurableOnly, failure.Phase)
			assert.Equal(t, test.cause, failure.Cause)
			durable, statusErr := repository.Status(context.Background(), server.ID)
			require.NoError(t, statusErr)
			require.NotNil(t, durable.Revision)
			assert.Equal(t, "1", *durable.Revision)
			assert.Equal(t, contract.ActiveCatalogAbsent, registry.Status(server.ID).State)
			assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
			page, listErr := registry.List(nil, 10)
			require.NoError(t, listErr)
			assert.Empty(t, page.Items)
			descriptors, listErr := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredExclude, nil, 10)
			require.NoError(t, listErr)
			require.Len(t, descriptors.Items, 1)
			_, routable := registry.Routes().Resolve(descriptors.Items[0].Resource.ID)
			assert.False(t, routable)
			if test.cause == PublicationFailureDrain {
				assert.False(t, registry.MarkUnavailableExact(server.ID, "runtime-1", 1, 1))
				assert.False(t, registry.WithdrawExact(server.ID, "runtime-1", 1, contract.ActiveCatalogUnavailable))
				assert.Equal(t, activeProcessID+"-0", registry.Summary().ActiveGeneration)
			}
		})
	}
}

func TestActiveRegistryCrashAfterCommitRestartsWithDurableOnlyEvidence(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	operation, err := serverRepository.CreateOperation(context.Background(), servers.OperationRequest{ServerID: server.ID, Kind: contract.OperationReload})
	require.NoError(t, err)
	_, err = serverRepository.TransitionOperation(context.Background(), operation.Operation.ID, contract.OperationRunning, nil)
	require.NoError(t, err)
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	registry.afterCommit = func() { panic("simulated crash") }
	assert.PanicsWithValue(t, "simulated crash", func() {
		_, _ = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true }})
	})
	durable, err := repository.Status(context.Background(), server.ID)
	require.NoError(t, err)
	require.NotNil(t, durable.Revision)
	assert.Equal(t, "1", *durable.Revision)

	restarted, err := NewActiveRegistry(repository, clock, "restarted")
	require.NoError(t, err)
	assert.Equal(t, contract.ActiveCatalogAbsent, restarted.Status(server.ID).State)
	assert.Equal(t, "restarted-0", restarted.Summary().ActiveGeneration)
	assert.Zero(t, restarted.Occupancy().InUse)
	require.NoError(t, serverRepository.InterruptNonterminal(context.Background()))
	interrupted, err := serverRepository.GetOperation(context.Background(), operation.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.OperationInterrupted, interrupted.State)
	assert.Equal(t, contract.ReasonInterrupted, *interrupted.Reason)
}

func TestActiveRegistryInstallBarrierKeepsRoutesAndSummaryOld(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	runtime, _ := newRouteRuntime(t)
	installEntered := make(chan struct{})
	releaseInstall := make(chan struct{})
	registry.beforeInstall = func() {
		durable, statusErr := repository.Status(context.Background(), server.ID)
		if assert.NoError(t, statusErr) && assert.NotNil(t, durable.Revision) {
			assert.Equal(t, "1", *durable.Revision)
		}
		assert.Equal(t, activeProcessID+"-0", registry.generationLocked())
		registry.routes.mu.RLock()
		assert.Empty(t, registry.routes.tools)
		registry.routes.mu.RUnlock()
		close(installEntered)
		<-releaseInstall
	}
	published := make(chan error, 1)
	go func() {
		_, publishErr := registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true }, Runtime: runtime})
		published <- publishErr
	}()
	<-installEntered
	close(releaseInstall)
	require.NoError(t, <-published)
	assert.Equal(t, activeProcessID+"-1", registry.Summary().ActiveGeneration)
	page, err := registry.List(nil, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	_, ok := registry.Routes().Resolve(page.Items[0].Resource.ID)
	assert.True(t, ok)
}

func TestActiveRegistryExactRuntimeGenerationFencesWithdrawal(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	runtime, _ := newRouteRuntime(t)
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 2, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true }, Runtime: runtime})
	require.NoError(t, err)
	page, err := registry.List(nil, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	capability, ok := registry.Routes().Resolve(page.Items[0].Resource.ID)
	require.True(t, ok)

	assert.False(t, registry.WithdrawExact(server.ID, "runtime-1", 1, contract.ActiveCatalogUnavailable))
	assert.Equal(t, contract.ActiveCatalogCurrent, registry.Status(server.ID).State)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	require.NoError(t, lease.Execute(context.Background(), json.RawMessage(`{}`)).Err)
	assert.True(t, registry.WithdrawExact(server.ID, "runtime-1", 2, contract.ActiveCatalogUnavailable))
	assert.Equal(t, contract.ActiveCatalogUnavailable, registry.Status(server.ID).State)
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

func TestActiveRouteWithdrawalCancelsCallsForEveryLifecycleLoss(t *testing.T) {
	for _, transition := range []struct {
		name  string
		state contract.ActiveCatalogState
	}{
		{name: "update", state: contract.ActiveCatalogUnavailable},
		{name: "disable", state: contract.ActiveCatalogAbsent},
		{name: "delete", state: contract.ActiveCatalogAbsent},
		{name: "disconnect", state: contract.ActiveCatalogUnavailable},
		{name: "session_failure", state: contract.ActiveCatalogUnavailable},
		{name: "authentication_failure", state: contract.ActiveCatalogUnavailable},
	} {
		t.Run(transition.name, func(t *testing.T) {
			repository, serverRepository, _, _ := newCatalogRepository(t)
			server := createCatalogServer(t, serverRepository, "sample")
			registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
			require.NoError(t, err)
			runtime, transport := newRouteRuntime(t)
			transport.callStarted = make(chan struct{})
			status, err := registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true }, Runtime: runtime})
			require.NoError(t, err)
			require.NotNil(t, status.Revision)
			page, err := registry.List(nil, 1)
			require.NoError(t, err)
			capability, ok := registry.Routes().Resolve(page.Items[0].Resource.ID)
			require.True(t, ok)
			lease, err := capability.Acquire(context.Background())
			require.NoError(t, err)
			result := make(chan downstream.CallResult, 1)
			go func() { result <- lease.Execute(context.Background(), json.RawMessage(`{}`)) }()
			<-transport.callStarted

			assert.True(t, registry.Withdraw(server.ID, "runtime-1", transition.state))
			_, ok = registry.Routes().Resolve(page.Items[0].Resource.ID)
			assert.False(t, ok)
			select {
			case call := <-result:
				assert.Equal(t, downstream.FailureStartUncertain, call.Failure)
				assert.ErrorIs(t, call.Err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("withdrawal did not cancel the active call")
			}
		})
	}
}

func TestProductionIngressDeniesAllAfterActiveRoutePublication(t *testing.T) {
	repository, serverRepository, _, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sample")
	registry, err := NewActiveRegistry(repository, &catalogClock{now: catalogTime}, activeProcessID)
	require.NoError(t, err)
	runtime, _ := newRouteRuntime(t)
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "sample", "one"), Current: func() bool { return true }, Runtime: runtime})
	require.NoError(t, err)
	page, err := registry.List(nil, 1)
	require.NoError(t, err)
	_, ok := registry.Routes().Resolve(page.Items[0].Resource.ID)
	require.True(t, ok)

	ingress := mcpingress.New(mcpingress.Options{Authenticator: mcpingress.DenyAllAuthenticator{}})
	defer ingress.Shutdown()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	_, err = ingress.Authenticate(context.Background(), request, contract.AuthorityAgent)
	assert.Error(t, err)
}

type routeTransport struct {
	mu          sync.Mutex
	messages    []downstream.Message
	callStarted chan struct{}
}

func (transport *routeTransport) Kind() downstream.TransportKind { return downstream.TransportHTTP }
func (transport *routeTransport) Exchange(ctx context.Context, message downstream.Message) (downstream.WireResponse, error) {
	if message.MarkHandoff != nil {
		message.MarkHandoff()
	}
	transport.mu.Lock()
	transport.messages = append(transport.messages, message)
	callStarted := transport.callStarted
	transport.mu.Unlock()
	if message.Method == "tools/call" && callStarted != nil {
		close(callStarted)
		<-ctx.Done()
		return downstream.WireResponse{}, ctx.Err()
	}
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

func TestActiveRegistryAggregateCountsStayConsistentAcrossTwoServers(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	serverA := createCatalogServer(t, serverRepository, "a")
	serverB := createCatalogServer(t, serverRepository, "b")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	candidate := candidateFor(t, serverA.ID, "a", "one", "two")
	candidate.Issues = []IssueClass{IssueDescriptorInvalid}
	status, err := registry.Publish(context.Background(), Publication{Fence: catalogFence(serverA.ID, "0"), RuntimeID: "runtime-a", RuntimeGeneration: 1, Candidate: candidate, Current: func() bool { return true }})
	require.NoError(t, err)
	assert.Equal(t, int64(2), status.ToolCount)
	assert.Equal(t, int64(1), status.IssueCount)
	assert.Equal(t, int64(2), registry.Occupancy().InUse)
	assert.True(t, registry.MarkUnavailableExact(serverB.ID, "runtime-b", 1, 2))
	summary := registry.Summary()
	assert.Equal(t, contract.AggregateCatalogDegraded, summary.ActiveState)
	assert.Equal(t, int64(3), summary.IssueCount)
	page, err := registry.List(nil, 10)
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, summary, page.Summary)

	assert.True(t, registry.WithdrawExact(serverA.ID, "runtime-a", 1, contract.ActiveCatalogUnavailable))
	assert.Equal(t, int64(0), registry.Occupancy().InUse)
	summary = registry.Summary()
	assert.Equal(t, contract.AggregateCatalogEmpty, summary.ActiveState)
	assert.Equal(t, int64(3), summary.IssueCount)
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
