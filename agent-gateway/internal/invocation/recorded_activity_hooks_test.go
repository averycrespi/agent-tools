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

func TestRecordedActivityHTTPSettlementAndSelection(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ring, clock := recordedFixture()
	s.recorded = ring
	request, err := s.AdmitHTTP(t.Context(), httpTrafficAdmission(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(t.Context(), request))
	intercepted := httpTrafficAdmission(2)
	intercepted.Target = &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}
	intercepted.Decision.Transport = contract.HTTPTransportIntercept
	intercepted.Decision.Reason = contract.HTTPReasonIntercept
	intercepted.Decision.Allowed = false
	selection, err := s.AdmitHTTP(t.Context(), intercepted)
	require.NoError(t, err)
	assert.False(t, s.Confirm(t.Context(), selection))
	s.Release(selection)
	invalid, err := s.AdmitHTTP(t.Context(), invalidHTTPAdmission(3))
	require.NoError(t, err)
	s.Release(invalid)
	clock.advance(time.Minute)
	completion := httpTrafficCompletion()
	completion.Outcome = "outcome_unknown"
	// A recorded 200 does not turn an uncertain transfer into success.
	require.NoError(t, s.CompleteHTTP(t.Context(), request, completion))
	require.Error(t, s.CompleteHTTP(t.Context(), request, completion))
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
	receipts := make([]*TrafficReceipt, 2)
	for i := range receipts {
		var err error
		receipts[i], err = s.Admit(t.Context(), trafficPrepared(i+1))
		require.NoError(t, err)
		require.True(t, s.Confirm(t.Context(), receipts[i]))
	}
	armed.Store(true)
	first, expired := make(chan error, 1), make(chan error, 1)
	go func() { first <- s.Complete(t.Context(), receipts[0], trafficCompletion()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal writer did not start")
	}
	go func() { expired <- s.Complete(t.Context(), receipts[1], trafficCompletion()) }()
	select {
	case queued := <-s.terminals:
		// The owned writer barrier makes this request exclusively ours. Exercise
		// expiry at eligibility without relying on scheduler delay or larger bounds.
		queued.expires = time.Now().Add(-time.Second)
		s.terminals <- queued
		require.Same(t, receipts[1], queued.receipt)
	case <-time.After(5 * time.Second):
		t.Fatal("second terminal did not queue")
	}
	unblock()
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first terminal did not settle")
	}
	select {
	case err := <-expired:
		require.ErrorIs(t, err, ErrTrafficDeadline)
	case <-time.After(5 * time.Second):
		t.Fatal("expired terminal did not settle")
	}
	require.NoError(t, t.Context().Err())
	status := s.Status(t.Context())
	assert.True(t, status.Ready)
	assert.False(t, status.Faulted)
	s.mu.Lock()
	pins, queued := len(s.pins), s.terminalQueued
	s.mu.Unlock()
	assert.Zero(t, pins)
	assert.Zero(t, queued)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	for _, record := range history.Records {
		if record.InvocationID == receipts[0].evidence.InvocationID {
			require.NotNil(t, record.TerminalClass)
			assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
		} else {
			assert.Equal(t, receipts[1].evidence.InvocationID, record.InvocationID)
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
	receipt, err := s.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(t.Context(), receipt))
	fail.Store(true)
	require.ErrorIs(t, s.Complete(t.Context(), receipt, trafficCompletion()), ErrTrafficFault)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.NotNil(t, history.Records[0].TerminalClass, "readable SQL is not an acknowledgment")
	clock.advance(time.Minute)
	counts := s.RecordedActivity().Buckets[14].Counts.MCP
	assert.Equal(t, uint64(1), counts.Admissions.Allow)
	assert.Equal(t, contract.RecordedCompletions{}, counts.Completions)
}
