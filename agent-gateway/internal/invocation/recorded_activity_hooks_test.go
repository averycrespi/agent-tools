package invocation

import (
	"errors"
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
