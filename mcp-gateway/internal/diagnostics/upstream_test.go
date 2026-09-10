package diagnostics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func upstreamExample(event Event) Facts {
	facts := Facts{Event: event, Upstream: 1, Attempt: 2, Phase: PhaseReconciliation, Reason: ReasonUnknown, Disposition: DispositionUnknown}
	switch event {
	case UpstreamAttemptStart:
		facts.Reason, facts.Disposition = ReasonNone, DispositionExecuting
	case UpstreamRetryScheduled:
		facts.Retry, facts.Delay, facts.Disposition = 1, time.Second, DispositionRetryScheduled
	case UpstreamRetryReset:
		facts.Disposition = DispositionCancelled
	case UpstreamRecovered:
		facts.Reason, facts.Disposition = ReasonNone, DispositionHealthy
	case OAuthRequired:
		facts.Phase, facts.Disposition = PhaseAuthorization, DispositionOperatorAuthentication
	case OAuthCompleted:
		facts.Phase, facts.Reason = PhaseAuthorization, ReasonNone
	case OAuthExpired, OAuthFailed:
		facts.Phase = PhaseAuthorization
	case OAuthRefreshComplete:
		facts.Phase, facts.Reason = PhaseRefresh, ReasonNone
	case OAuthRefreshFailed:
		facts.Phase = PhaseRefresh
	case CatalogPollScheduled:
		facts.Phase, facts.Delay, facts.Disposition = PhaseToolDiscovery, contract.CatalogPollInterval, DispositionRetryScheduled
	case OAuthStage:
		facts.Phase, facts.Reason, facts.Disposition = PhaseOAuthExchange, ReasonNone, DispositionExecuting
	}
	return facts
}

func TestUpstreamDiagnosticClosedVocabulary(t *testing.T) {
	require.Equal(t, contract.DiagnosticUpstreamPhases(), phaseNames[:])
	require.Equal(t, contract.DiagnosticUpstreamReasons(), reasonNames[:])
	require.Equal(t, contract.DiagnosticUpstreamDispositions(), dispositionNames[:])
	for _, event := range []Event{Startup, InvocationAdmission, StorageWait} {
		facts := validEventExample(event)
		facts.Upstream = 1
		require.False(t, validFacts(facts), "references cannot escape the upstream inventory")
	}
}

func TestUpstreamDiagnosticLevelSelection(t *testing.T) {
	for _, level := range []Level{Warn, Info, Debug} {
		t.Run([]string{"warn", "info", "debug"}[level], func(t *testing.T) {
			var output bytes.Buffer
			adapter := New(&output, level)
			for event := UpstreamAttemptStart; event <= CatalogPollScheduled; event++ {
				adapter.Reconciliation(upstreamExample(event))
			}
			require.True(t, adapter.Finish(nil))
			got := records(t, output.Bytes())
			for event := UpstreamAttemptStart; event <= CatalogPollScheduled; event++ {
				found := false
				for _, record := range got {
					if record["event"] == eventNames[event] {
						found = true
					}
				}
				require.Equal(t, level >= upstreamLevel(event), found, eventNames[event])
			}
		})
	}
}

func TestCatalogScheduleDiagnosticBounds(t *testing.T) {
	facts := upstreamExample(CatalogPollScheduled)
	require.True(t, validFacts(facts))
	facts.Delay = contract.CatalogPollInterval + time.Nanosecond
	require.False(t, validFacts(facts))
	facts.Delay = 0
	require.False(t, validFacts(facts))
	facts.Delay, facts.Retry = time.Second, 1
	require.False(t, validFacts(facts), "periodic catalog polls have no reconciliation retry ordinal")
}

func TestUpstreamSuppressionAndRecoveryAtWarn(t *testing.T) {
	var output bytes.Buffer
	adapter := New(&output, Warn)
	now := time.Unix(1, 0)
	adapter.now = func() time.Time { return now }
	failure := upstreamExample(UpstreamUnhealthy)
	failure.Phase, failure.Reason, failure.Disposition = PhaseConnection, ReasonConnectivity, DispositionRetryScheduled
	adapter.Reconciliation(failure)
	for range 3 {
		adapter.Reconciliation(failure)
	}
	other := failure
	other.Upstream = 2
	adapter.Reconciliation(other)
	now = now.Add(contract.DiagnosticSummaryInterval)
	adapter.Reconciliation(failure)
	adapter.Reconciliation(upstreamExample(UpstreamRecovered)) // Filtered at warn, but must reset suppression.
	adapter.Reconciliation(failure)
	require.True(t, adapter.Finish(nil))
	got := records(t, output.Bytes())
	require.Len(t, got, 4)
	require.EqualValues(t, 2, got[1]["upstream_ref"])
	require.EqualValues(t, 3, got[2]["suppressed"])
	require.NotContains(t, got[3], "suppressed")
	require.Empty(t, adapter.suppression)
}

func TestUpstreamMalformedFactsAndSecretCanaries(t *testing.T) {
	canary := "PRIVATE-token-url-header-code-state-pkce-tool-argument-stderr"
	var output bytes.Buffer
	adapter := New(&output, Debug)
	for _, mutate := range []func(*Facts){
		func(f *Facts) { f.InvocationID = canary },
		func(f *Facts) { f.Phase = 255 }, func(f *Facts) { f.Reason = 255 },
		func(f *Facts) { f.Disposition = 255 }, func(f *Facts) { f.Duration = -1 },
		func(f *Facts) { f.Duration = contract.DiagnosticElapsedMaximum + 1 },
		func(f *Facts) { f.Suppressed = 1 }, func(f *Facts) { f.Delay = time.Hour },
		func(f *Facts) { f.Mutation = 1 },
	} {
		facts := upstreamExample(UpstreamAttemptComplete)
		mutate(&facts)
		require.False(t, validFacts(facts))
		adapter.Reconciliation(facts)
	}
	raw := contract.PublicReason(canary)
	unknown := upstreamExample(UpstreamUnhealthy)
	unknown.Reason = PublicReason(&raw)
	adapter.Reconciliation(unknown)
	require.True(t, adapter.Finish(nil))
	require.NotContains(t, output.String(), canary)
	require.Contains(t, output.String(), `"reason":"unknown"`)
	for _, record := range records(t, output.Bytes()) {
		for key := range record {
			require.False(t, strings.Contains(key, "resource") || strings.Contains(key, "credential"))
		}
	}
}

func TestUpstreamNoiseStateBoundAndElapsed(t *testing.T) {
	adapter := &Adapter{suppression: make(map[suppressionKey]suppressionState), now: func() time.Time { return time.Unix(1, 0) }}
	for ref := uint64(1); ref <= contract.DiagnosticSuppressionOwners+2; ref++ {
		facts := upstreamExample(UpstreamUnhealthy)
		facts.Upstream = ref
		require.False(t, adapter.suppress(&facts))
	}
	require.Len(t, adapter.suppression, contract.DiagnosticSuppressionOwners)
	start := time.Unix(1, 0)
	require.Zero(t, Elapsed(start, start.Add(-time.Second)))
	require.Equal(t, contract.DiagnosticElapsedMaximum, Elapsed(start, start.Add(48*time.Hour)))
}
