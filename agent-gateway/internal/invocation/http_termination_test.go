package invocation

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestTrafficHTTPTerminationHistoricalAndRestart(t *testing.T) {
	_, repository, authority, _, _ := newAdmissionCoordinator(t, nil)
	s, owner := trafficFixture(t, nil, nil)
	repository.traffic = s
	reader, err := NewReadService(repository, authority)
	require.NoError(t, err)
	clean := httpTrafficCompletion()
	clean.ResponseSource = "upstream"
	clean.Termination = &contract.HTTPTermination{Stage: "complete", Condition: "clean"}
	incomplete := clean
	incomplete.Outcome = "outcome_unknown"
	incomplete.Termination = &contract.HTTPTermination{Stage: "upstream_read", Condition: "failure", Context: "cancelled"}
	legacy := httpTrafficCompletion()
	expected := []*contract.HTTPTrafficCompletion{nil, &legacy, &incomplete, &clean}
	for i, c := range expected {
		receipt, e := s.AdmitHTTP(t.Context(), httpTrafficAdmission(i+1))
		require.NoError(t, e)
		if c == nil {
			s.Release(receipt)
		} else {
			require.True(t, s.Confirm(t.Context(), receipt))
			require.NoError(t, s.CompleteHTTP(t.Context(), receipt, *c))
		}
	}
	page, err := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	for i, c := range expected {
		item, e := reader.GetHTTP(t.Context(), invocationID(i+1))
		require.NoError(t, e)
		require.Equal(t, c, item.Completion)
		row := page.Items[3-i]
		require.Equal(t, c != nil, row.CompletionRecorded)
		if c == nil {
			require.Nil(t, row.Termination)
		} else {
			require.Equal(t, c.Termination, row.Termination)
		}
	}
	canonical, err := encodeHTTPCompletion(httpTrafficAdmission(2), legacy)
	require.NoError(t, err)
	require.NotContains(t, canonical, "termination")
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err := reopened.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	for i, c := range expected {
		require.Equal(t, c, history.Records[i].Completion)
	}
}

func TestTrafficHTTPTerminationRejectsFabricatedAndOversizedFacts(t *testing.T) {
	a := httpTrafficAdmission(1)
	c := httpTrafficCompletion()
	c.ResponseSource = "upstream"
	c.Outcome = "outcome_unknown"
	c.Termination = &contract.HTTPTermination{Stage: "downstream_flush", Condition: "failure", Context: "timeout"}
	c.BytesSent = math.MaxInt64
	c.BytesReceived = math.MaxInt64
	c.DurationMS = math.MaxInt64
	raw, err := encodeHTTPCompletion(a, c)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), contract.HTTPTrafficCompletionBytes)
	for _, fact := range []contract.HTTPTermination{
		{Stage: "secret", Condition: "failure"}, {Stage: "upstream_read", Condition: "secret"},
		{Stage: "upstream_read", Condition: "failure", Context: "client_cancelled"},
		{Stage: "complete", Condition: "clean"}, {Stage: "complete", Condition: "failure"},
		{Stage: "exchange", Condition: "failure"}, {Stage: "response_headers", Condition: "cancelled"},
	} {
		c.Termination = &fact
		_, err = encodeHTTPCompletion(a, c)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	c.Termination = &contract.HTTPTermination{Stage: "complete", Condition: "clean", Context: "cancelled"}
	c.Outcome = "succeeded"
	_, err = encodeHTTPCompletion(a, c)
	require.ErrorIs(t, err, ErrInvalidInput)
	c.Termination.Context = ""
	_, err = encodeHTTPCompletion(a, c)
	require.NoError(t, err)
	// Closed JSON also rejects unrecognized fields instead of retaining raw errors.
	encoded, err := json.Marshal(c)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), contract.HTTPTrafficCompletionBytes)
}
