package downstream

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatcherAcquiresGlobalThenServerWithoutWaitingAndReleasesGlobalOnServerSaturation(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	binding := testBinding("server-1", "tool-1")
	capability := testCapability(t, dispatcher, binding, Current)
	leases := make([]*Lease, 0, 4)
	for range 4 {
		lease, acquireErr := capability.Acquire(context.Background())
		require.NoError(t, acquireErr)
		leases = append(leases, lease)
	}
	assert.Equal(t, contract.LimitStatus{InUse: 4, Limit: 4, Saturated: true}, dispatcher.ServerStatus("server-1"))
	assert.Equal(t, int64(4), dispatcher.Status().InUse)
	_, err = capability.Acquire(context.Background())
	assertPreStartReason(t, err, RejectionServerSaturated)
	assert.Equal(t, int64(4), dispatcher.Status().InUse, "global permit leaked on server saturation")
	for _, lease := range leases {
		require.NoError(t, lease.Cancel(context.Background()))
	}
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
	assert.Equal(t, int64(0), dispatcher.ServerStatus("server-1").InUse)
}

func TestDispatcherGlobalNAndNPlusOneAndGlobalFirstOrdering(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	var leases []*Lease
	for index := range 32 {
		serverID := "server-" + string(rune('a'+index/4))
		capability := testCapability(t, dispatcher, testBinding(serverID, "tool"), Current)
		lease, acquireErr := capability.Acquire(context.Background())
		require.NoError(t, acquireErr)
		leases = append(leases, lease)
	}
	assert.True(t, dispatcher.Status().Saturated)
	capability := testCapability(t, dispatcher, testBinding("server-a", "other"), Current)
	_, err = capability.Acquire(context.Background())
	assertPreStartReason(t, err, RejectionGlobalSaturated)
	for _, lease := range leases {
		require.NoError(t, lease.Cancel(context.Background()))
	}
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
}

func TestDispatcherDoesNotRetainIdleServerAdmissionState(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	for index := range 128 {
		serverID := "server-" + string(rune(index+1))
		capability := testCapability(t, dispatcher, testBinding(serverID, "tool"), Current)
		lease, acquireErr := capability.Acquire(context.Background())
		require.NoError(t, acquireErr)
		require.NoError(t, lease.Cancel(context.Background()))
	}
	dispatcher.mu.Lock()
	retained := len(dispatcher.servers)
	dispatcher.mu.Unlock()
	assert.Zero(t, retained)
}

func TestCapabilityRevalidatesEveryBoundRevisionAfterPermits(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	binding := testBinding("server-1", "tool-1")
	validated := false
	capability, err := dispatcher.Publish(binding, runtimeForCall(t, EraModern, "", &callTransport{kind: TransportStdio}), func(_ context.Context, observed Binding) Availability {
		assert.Equal(t, binding, observed)
		assert.Equal(t, int64(1), dispatcher.Status().InUse)
		assert.Equal(t, int64(1), dispatcher.ServerStatus(binding.ServerID).InUse)
		validated = true
		return Current
	})
	require.NoError(t, err)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	assert.True(t, validated)
	require.NoError(t, lease.Cancel(context.Background()))
}

func TestCapabilityRejectsEveryNoncurrentStateAfterPermitAcquisition(t *testing.T) {
	tests := []struct {
		availability Availability
		reason       RejectionReason
	}{
		{availability: Stale, reason: RejectionStale},
		{availability: Draining, reason: RejectionDraining},
		{availability: Unavailable, reason: RejectionUnavailable},
	}
	for _, test := range tests {
		t.Run(string(test.availability), func(t *testing.T) {
			dispatcher, err := NewDispatcher()
			require.NoError(t, err)
			capability := testCapability(t, dispatcher, testBinding("server-1", "tool"), test.availability)
			_, err = capability.Acquire(context.Background())
			assertPreStartReason(t, err, test.reason)
			assert.Equal(t, int64(0), dispatcher.Status().InUse)
			assert.Equal(t, int64(0), dispatcher.ServerStatus("server-1").InUse)
		})
	}
}

func TestWithdrawalDuringPostPermitRevalidationRejectsAndCancelsLeases(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	capability, err := dispatcher.Publish(testBinding("server-1", "tool"), runtimeForCall(t, EraModern, "", &callTransport{kind: TransportStdio}), func(context.Context, Binding) Availability {
		close(entered)
		<-release
		return Current
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, acquireErr := capability.Acquire(context.Background())
		done <- acquireErr
	}()
	<-entered
	require.NoError(t, capability.Withdraw(context.Background()))
	close(release)
	assertPreStartReason(t, <-done, RejectionWithdrawn)
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
	_, err = capability.Acquire(context.Background())
	assertPreStartReason(t, err, RejectionWithdrawn)
}

func TestLeaseReleasesServerBeforeGlobal(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	serverReleased := make(chan struct{})
	continueRelease := make(chan struct{})
	dispatcher.releaseObserver = func(scope string) {
		if scope == "server" {
			close(serverReleased)
			<-continueRelease
		}
	}
	capability := testCapability(t, dispatcher, testBinding("server-1", "tool"), Current)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- lease.Cancel(context.Background()) }()
	<-serverReleased
	assert.Equal(t, int64(0), dispatcher.ServerStatus("server-1").InUse)
	assert.Equal(t, int64(1), dispatcher.Status().InUse)
	close(continueRelease)
	require.NoError(t, <-done)
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
}

func TestLeaseExecutesExactlyOnceAndReleasesInServerThenGlobalOrder(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	transport := &callTransport{kind: TransportStdio, response: callSuccess(1)}
	capability, err := dispatcher.Publish(testBinding("server-1", "tool-1"), runtimeForCall(t, EraModern, "", transport), func(context.Context, Binding) Availability { return Current })
	require.NoError(t, err)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	result := lease.Execute(context.Background(), json.RawMessage(`{"value":7}`))
	require.NoError(t, result.Err)
	assert.Equal(t, int64(0), dispatcher.ServerStatus("server-1").InUse)
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
	second := lease.Execute(context.Background(), json.RawMessage(`{"value":8}`))
	assert.ErrorIs(t, second.Err, ErrLeaseConsumed)
	assert.Equal(t, FailurePreStart, second.Failure)
	assert.Len(t, transport.messages, 1)
	assert.Contains(t, string(transport.messages[0].Payload), `"name":"upstream.tool"`)
}

func TestLeaseCancellationBeforeExecutionReleasesAndRemainsPreStart(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	transport := &callTransport{kind: TransportStdio}
	capability, err := dispatcher.Publish(testBinding("server-1", "tool-1"), runtimeForCall(t, EraModern, "", transport), func(context.Context, Binding) Availability { return Current })
	require.NoError(t, err)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	require.NoError(t, lease.Cancel(context.Background()))
	result := lease.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Equal(t, FailurePreStart, result.Failure)
	assert.ErrorIs(t, result.Err, ErrLeaseCancelled)
	assert.Empty(t, transport.messages)
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
}

func TestLeaseCancellationPreservesCallMarkerClassificationWithoutReplay(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	transport := &callTransport{kind: TransportStdio, handedOff: make(chan struct{}), waitForCancel: true}
	capability, err := dispatcher.Publish(testBinding("server-1", "tool-1"), runtimeForCall(t, EraModern, "", transport), func(context.Context, Binding) Availability { return Current })
	require.NoError(t, err)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	done := make(chan CallResult, 1)
	go func() { done <- lease.Execute(context.Background(), json.RawMessage(`{}`)) }()
	<-transport.handedOff
	require.NoError(t, lease.Cancel(context.Background()))
	result := <-done
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.ErrorIs(t, result.Err, context.Canceled)
	assert.Len(t, transport.messages, 1)
	assert.Len(t, transport.notifications, 1)
	assert.Equal(t, int64(0), dispatcher.Status().InUse)
}

func TestCapabilityAndLeaseAreOpaqueAndNonserializable(t *testing.T) {
	dispatcher, err := NewDispatcher()
	require.NoError(t, err)
	capability := testCapability(t, dispatcher, testBinding("server-1", "tool"), Current)
	_, err = json.Marshal(capability)
	assert.ErrorIs(t, err, ErrCapabilitySerialization)
	lease, err := capability.Acquire(context.Background())
	require.NoError(t, err)
	_, err = json.Marshal(lease)
	assert.ErrorIs(t, err, ErrCapabilitySerialization)
	require.NoError(t, lease.Cancel(context.Background()))
}

func TestProductionHasNoCapabilityConsumerOrAgentVisibleCall(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	gateway := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, relative := range []string{"cmd", "internal/api", "internal/mcpingress"} {
		err := filepath.WalkDir(filepath.Join(gateway, relative), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return walkErr
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			assert.NotContains(t, string(contents), "internal/downstream", path)
			assert.NotContains(t, string(contents), "NewDispatcher(", path)
			return nil
		})
		require.NoError(t, err)
	}
}

func testCapability(t *testing.T, dispatcher *Dispatcher, binding Binding, availability Availability) *Capability {
	t.Helper()
	capability, err := dispatcher.Publish(binding, runtimeForCall(t, EraModern, "", &callTransport{kind: TransportStdio}), func(context.Context, Binding) Availability { return availability })
	require.NoError(t, err)
	return capability
}

func testBinding(serverID, toolID string) Binding {
	return Binding{
		ServerID:            serverID,
		ToolID:              toolID,
		UpstreamName:        "upstream.tool",
		RuntimeID:           "runtime-1",
		DesiredRevision:     "desired-1",
		CredentialRevisions: contract.CredentialRevisions{StaticCredential: "static-1", OAuthClient: "client-1", OAuthTokens: "tokens-1"},
		CatalogRevision:     "catalog-1",
	}
}

func assertPreStartReason(t *testing.T, err error, reason RejectionReason) {
	t.Helper()
	var rejection *PreStartRejection
	require.ErrorAs(t, err, &rejection)
	assert.Equal(t, reason, rejection.Reason)
	assert.Equal(t, FailurePreStart, rejection.Failure)
}
