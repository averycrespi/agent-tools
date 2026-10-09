package diagnostics

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrafficLossSaturatesWithoutPerSubmissionQueueRecords(t *testing.T) {
	var output bytes.Buffer
	adapter := New(&output, Warn)
	for range QueueRecords + 1 {
		adapter.TrafficLoss(TrafficInvalid, false, ^uint64(0))
	}
	adapter.TrafficLoss(TrafficInvalid, true, 2)
	require.Zero(t, adapter.Status().Accepted)
	require.True(t, adapter.Finish(nil))
	require.Contains(t, output.String(), "reason=invalid initial_submissions=9007199254740991 terminal_submissions=2")
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("\n")))
	require.LessOrEqual(t, output.Len(), RecordBytes)
}
