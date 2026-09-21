package diagnostics

import (
	"bytes"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticProcessIdentityDoesNotUseWallClock(t *testing.T) {
	clock := func() time.Time { return time.Unix(1, 0) }
	var outputs [2]bytes.Buffer
	var identities [2]string
	for index := range outputs {
		entropy := bytes.NewReader(bytes.Repeat([]byte{byte(index + 1)}, 16))
		adapter := newAdapter(&outputs[index], Warn, entropy, clock)
		identities[index] = adapter.ProcessID()
		adapter.Reconciliation(upstreamExample(UpstreamUnhealthy)) // Same reference in both simulated processes.
		require.True(t, adapter.Finish(nil))
		require.Equal(t, identities[index], records(t, outputs[index].Bytes())[0]["process_id"])
		require.Len(t, identities[index], 32)
	}
	require.NotEqual(t, identities[0], identities[1], "identical/rolled-back clocks must not determine process identity")
	var output bytes.Buffer
	unavailable := newAdapter(&output, Warn, bytes.NewReader(nil), clock)
	require.Empty(t, unavailable.ProcessID(), "entropy failure omits correlation without clock fallback")
	require.True(t, unavailable.Finish(nil))
}

func TestWarningGuidanceUsesClosedFacts(t *testing.T) {
	for _, test := range []struct {
		facts  Facts
		action string
	}{
		{Facts{Event: LifecycleFailure, Cause: Unavailable}, "inspect_status"},
		{Facts{Event: StorageLatch, Mutation: 1, Cause: Latched, Stage: IntentCleanup}, "storage_recovery"},
		{Facts{Event: ReconciliationSettlementFailure, Cause: Stopped}, "inspect_settlement_no_replay"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonUnknown}, "inspect_status"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonConnectivity}, "inspect_connection"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonConfigurationInvalid}, "inspect_configuration"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonKeyringLocked}, "inspect_credentials"},
		{Facts{Event: UpstreamUnhealthy, Disposition: DispositionOperatorAuthentication}, "authorize_upstream"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonAuthenticationRejected, Disposition: DispositionRetryScheduled}, "wait_scheduled_retry"},
		{Facts{Event: UpstreamUnhealthy, Reason: ReasonStopUnconfirmed, Disposition: DispositionRetryScheduled}, "inspect_settlement_no_replay"},
	} {
		var output bytes.Buffer
		adapter := New(&output, Warn)
		adapter.Observe(test.facts)
		require.True(t, adapter.Finish(nil))
		got := records(t, output.Bytes())
		require.Len(t, got, 1)
		require.Equal(t, test.action, got[0]["action"])
		require.Contains(t, contract.DiagnosticActions(), got[0]["action"])
	}
}

func TestRecoveryRetainsIncidentAcrossTransitionsAndOAuthSuccess(t *testing.T) {
	var output bytes.Buffer
	adapter := New(&output, Warn)
	now := time.Unix(1, 0)
	adapter.now = func() time.Time { return now }
	failure := upstreamExample(OAuthRefreshFailed)
	adapter.Reconciliation(failure)
	adapter.Reconciliation(failure)
	now = now.Add(time.Second)
	adapter.Reconciliation(upstreamExample(OAuthRefreshComplete))
	adapter.Reconciliation(upstreamExample(OAuthCompleted))
	adapter.Reconciliation(failure) // Successful refresh resets noise, not incident.
	adapter.Reconciliation(failure)
	now = now.Add(time.Second)
	failure.Event, failure.Phase = UpstreamUnhealthy, PhaseConnection
	adapter.Reconciliation(failure)
	adapter.Reconciliation(failure)
	failure.Reason = ReasonConnectivity
	adapter.Reconciliation(failure) // A transition preserves incident start/count.
	adapter.Reconciliation(failure)
	now = now.Add(48 * time.Hour)
	adapter.Reconciliation(upstreamExample(UpstreamRecovered))
	adapter.Reconciliation(upstreamExample(UpstreamRecovered))
	require.True(t, adapter.Finish(nil))
	got := records(t, output.Bytes())
	require.Len(t, got, 5)
	recovered := got[4]
	require.Equal(t, "upstream_recovered", recovered["event"])
	require.EqualValues(t, 4, recovered["suppressed"])
	require.EqualValues(t, contract.DiagnosticElapsedMaximum.Milliseconds(), recovered["duration_ms"])
}

func TestRecoveryQueueLossDoesNotReplay(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	adapter := New(sink, Warn)
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
		adapter.Finish(nil)
		<-adapter.Done()
	})
	adapter.Reconciliation(upstreamExample(UpstreamUnhealthy))
	<-sink.entered
	for range QueueRecords - 1 {
		adapter.Observe(Facts{Event: LifecycleFailure, Cause: Unavailable})
	}
	adapter.Reconciliation(upstreamExample(UpstreamRecovered))
	require.EqualValues(t, 1, adapter.dropped.Load())
	require.Empty(t, adapter.suppression, "queue acceptance is not incident authority")
	adapter.Reconciliation(upstreamExample(UpstreamRecovered))
	require.EqualValues(t, 1, adapter.dropped.Load(), "healthy observations do not replay a lost recovery")
	close(sink.release)
	require.True(t, adapter.Finish(nil))
	for _, record := range records(t, sink.buffer.Bytes()) {
		require.NotEqual(t, "upstream_recovered", record["event"])
	}
}

func TestRecoveryUnavailableAfterLostOrEvictedHistory(t *testing.T) {
	adapter := &Adapter{suppression: make(map[suppressionKey]suppressionState), now: func() time.Time { return time.Unix(1, 0) }}
	recovery := upstreamExample(UpstreamRecovered)
	require.True(t, adapter.suppress(&recovery), "fresh process has no history")
	uncorrelated := upstreamExample(UpstreamUnhealthy)
	uncorrelated.Upstream = 0
	require.False(t, adapter.suppress(&uncorrelated))
	recovery.Upstream = 0
	require.True(t, adapter.suppress(&recovery))
	first := upstreamExample(UpstreamUnhealthy)
	require.False(t, adapter.suppress(&first))
	adapter.now = func() time.Time { return time.Unix(2, 0) }
	for ref := uint64(2); ref <= contract.DiagnosticSuppressionOwners+1; ref++ {
		failure := first
		failure.Upstream = ref
		require.False(t, adapter.suppress(&failure))
	}
	recovery.Upstream = 1
	require.True(t, adapter.suppress(&recovery), "eviction cannot manufacture recovery")
}
