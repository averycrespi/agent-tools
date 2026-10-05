package invocation

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordedActivityGitDoesNotFabricateMCPOrHTTPCounts(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ring, clock := recordedFixture()
	s.recorded = ring
	git := recordGit(t, s, gitTrafficAdmission(1))
	denied := gitTrafficAdmission(2)
	denied.Allowed = false
	recordGit(t, s, denied)
	clock.advance(time.Minute)
	result := s.RecordedActivity()
	require.Equal(t, "process_start", result.EpochReason)
	require.NotNil(t, result.Buckets[14].Counts)
	assert.Equal(t, contract.RecordedProtocols{}, *result.Buckets[14].Counts)

	recordGitCompletion(t, s, git, gitTrafficCompletion())
	recordGitCompletion(t, s, git, gitTrafficCompletion())
	clock.advance(time.Minute)
	result = s.RecordedActivity()
	require.Equal(t, "process_start", result.EpochReason)
	require.NotNil(t, result.Buckets[14].Counts)
	assert.Equal(t, contract.RecordedProtocols{}, *result.Buckets[14].Counts)
	history, err := s.GitHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	require.NotNil(t, history.Records[0].Completion)
	assert.Equal(t, "outcome_unknown", history.Records[0].Completion.Outcome)
	assert.Nil(t, history.Records[1].Completion)

	mcp := recordMCP(t, s, trafficPrepared(3))
	recordMCPCompletion(t, s, mcp, trafficCompletion())
	http := recordHTTP(t, s, httpTrafficAdmission(4))
	recordHTTPCompletion(t, s, http, httpTrafficCompletion())
	clock.advance(time.Minute)
	counts := s.RecordedActivity().Buckets[14].Counts
	require.NotNil(t, counts)
	assert.Equal(t, contract.RecordedEvents{Admissions: contract.RecordedAdmissions{Allow: 1}, Completions: contract.RecordedCompletions{Succeeded: 1}}, counts.MCP)
	assert.Equal(t, counts.MCP, counts.HTTPRequest)
	assert.Equal(t, contract.RecordedEvents{}, counts.Connect)
	assert.Equal(t, contract.RecordedEvents{}, counts.HTTPUnclassified)
}

func TestRecordedActivityHTTPSettlementAndSelection(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ring, clock := recordedFixture()
	s.recorded = ring
	request := recordHTTP(t, s, httpTrafficAdmission(1))
	intercepted := httpTrafficAdmission(2)
	intercepted.Target = &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}
	intercepted.Decision.Transport = contract.HTTPTransportIntercept
	intercepted.Decision.Reason = contract.HTTPReasonIntercept
	intercepted.Decision.Allowed = false
	recordHTTP(t, s, intercepted)
	recordHTTP(t, s, invalidHTTPAdmission(3))
	clock.advance(time.Minute)
	completion := httpTrafficCompletion()
	completion.Outcome = "outcome_unknown"
	// A recorded 200 does not turn an uncertain transfer into success.
	recordHTTPCompletion(t, s, request, completion)
	recordHTTPCompletion(t, s, request, completion)
	clock.advance(time.Minute)
	result := s.RecordedActivity()
	assert.Equal(t, uint64(1), result.Buckets[13].Counts.HTTPRequest.Admissions.Allow)
	assert.Equal(t, uint64(1), result.Buckets[13].Counts.Connect.Admissions.InterceptionSelected)
	assert.Equal(t, contract.RecordedCompletions{}, result.Buckets[13].Counts.Connect.Completions)
	assert.Equal(t, uint64(1), result.Buckets[13].Counts.HTTPUnclassified.Admissions.InvalidRequest)
	assert.Equal(t, uint64(1), result.Buckets[14].Counts.HTTPRequest.Completions.OutcomeUnknown)
	assert.Zero(t, result.Buckets[14].Counts.HTTPRequest.Completions.Succeeded)
}

func TestRecordedActivityExpiredTerminalIsNotAcknowledged(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var blockOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "before_begin" && armed.Load() {
			blockOnce.Do(func() { close(entered); <-release })
		}
		return nil
	})
	defer unblock()
	ring, clock := recordedFixture()
	s.recorded = ring
	observations := []*TrafficObservation{recordMCP(t, s, trafficPrepared(1)), recordMCP(t, s, trafficPrepared(2))}
	armed.Store(true)
	require.NoError(t, s.ObserveMCPCompletion(observations[0], trafficCompletion(), nil))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal writer did not start")
	}
	require.NoError(t, s.ObserveMCPCompletion(observations[1], trafficCompletion(), nil))
	select {
	case queued := <-s.observations:
		// The owned writer barrier makes this request exclusively ours. Exercise
		// expiry at eligibility without relying on scheduler delay or larger bounds.
		queued.expires = time.Now().Add(-time.Second)
		s.observations <- queued
		require.Equal(t, observations[1].prepared.InvocationID, queued.observation.prepared.InvocationID)
	case <-time.After(5 * time.Second):
		t.Fatal("second terminal did not queue")
	}
	unblock()
	waitTraffic(t, s)
	require.NoError(t, t.Context().Err())
	status := s.Status(t.Context())
	assert.True(t, status.Ready)
	assert.False(t, status.Faulted)
	s.mu.Lock()
	queued := s.queued
	s.mu.Unlock()
	assert.Zero(t, queued)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	for _, record := range history.Records {
		if record.InvocationID == observations[0].prepared.InvocationID {
			require.NotNil(t, record.TerminalClass)
			assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
		} else {
			assert.Equal(t, observations[1].prepared.InvocationID, record.InvocationID)
			assert.Nil(t, record.TerminalClass)
		}
	}
	clock.advance(time.Minute)
	counts := s.RecordedActivity().Buckets[14].Counts.MCP
	assert.Equal(t, uint64(2), counts.Admissions.Allow)
	assert.Equal(t, contract.RecordedCompletions{Succeeded: 1}, counts.Completions)
}

func TestRecordedActivityUncertainTerminalIsNotAcknowledged(t *testing.T) {
	var fail atomic.Bool
	s, _ := trafficFixture(t, nil, func(point string) error {
		if fail.Load() && point == "acknowledgment" {
			return errors.New("lost acknowledgment")
		}
		return nil
	})
	ring, clock := recordedFixture()
	s.recorded = ring
	observation := recordMCP(t, s, trafficPrepared(1))
	fail.Store(true)
	require.NoError(t, s.ObserveMCPCompletion(observation, trafficCompletion(), nil))
	waitTraffic(t, s)
	require.False(t, s.Healthy())
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.NotNil(t, history.Records[0].TerminalClass, "readable SQL is not an acknowledgment")
	clock.advance(time.Minute)
	counts := s.RecordedActivity().Buckets[14].Counts.MCP
	assert.Equal(t, uint64(1), counts.Admissions.Allow)
	assert.Equal(t, contract.RecordedCompletions{}, counts.Completions)
}
