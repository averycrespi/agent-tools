package invocation

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func captureLossDiagnostics(t *testing.T, s *TrafficStore) func() string {
	t.Helper()
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	s.SetTrafficDiagnostics(adapter)
	return func() string { require.True(t, adapter.Finish(nil)); return output.String() }
}

func TestTrafficLossSummarySeparatesDisabledUnavailableInvalidAndOversized(t *testing.T) {
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
	disabled := NewOptionalTraffic(DefaultTrafficConfig())
	disabled.SetTrafficDiagnostics(adapter)
	disabled.StartOpening(nil)
	t.Cleanup(func() { require.NoError(t, disabled.Close()) })
	invalid := trafficPrepared(1)
	invalid.admission.MCP.RedactedArguments = []byte("private-payload-canary")
	require.Nil(t, disabled.ObserveMCP(invalid))
	observation := disabled.ObserveMCP(trafficPrepared(2))
	require.NotNil(t, observation)
	require.Error(t, disabled.ObserveMCPCompletion(observation, trafficCompletion(), nil))
	target, _ := trafficFixture(t, nil, nil)
	opening := NewOptionalTraffic(DefaultTrafficConfig())
	opening.SetTrafficDiagnostics(adapter)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { unblock(); require.NoError(t, opening.Close()) })
	opening.StartOpening(func() (*TrafficStore, error) { <-release; return target, nil })
	require.NotNil(t, opening.ObserveMCP(trafficPrepared(3)))
	unblock()
	require.Eventually(t, func() bool { return opening.Healthy() }, time.Second, time.Millisecond)
	require.Nil(t, opening.ObserveMCP(invalid))
	require.ErrorIs(t, opening.enqueueObservation(&trafficRequest{observation: *observation, bytes: target.config.BatchBytes + 1}), ErrTrafficCapacity)
	require.NotNil(t, opening.ObserveMCP(trafficPrepared(4)))
	waitTraffic(t, target)
	require.True(t, adapter.Finish(nil))
	for _, fact := range []string{"reason=invalid initial_submissions=2 terminal_submissions=0", "reason=capture_disabled initial_submissions=1 terminal_submissions=1", "reason=history_unavailable initial_submissions=1 terminal_submissions=0", "reason=oversized initial_submissions=1 terminal_submissions=0", "submissions_are_not_executions_or_distinct_rows"} {
		require.Contains(t, output.String(), fact)
	}
	require.NotContains(t, output.String(), "private-payload-canary")
}
