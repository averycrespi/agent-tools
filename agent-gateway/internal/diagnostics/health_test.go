package diagnostics

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failedHealthSink struct{}

func (failedHealthSink) Write([]byte) (int, error) { return 0, errors.New("secret-canary") }

func TestDeliveryHealthSurvivesFailedSink(t *testing.T) {
	a := New(failedHealthSink{}, Debug)
	a.Observe(Facts{Event: 255})
	a.Observe(Facts{Event: Startup})
	<-a.Done()
	s := a.Status()
	require.Equal(t, "failed", s.State)
	require.EqualValues(t, 1, s.Invalid)
	require.EqualValues(t, 1, s.WriteFailures)
	require.Zero(t, s.Written)
	require.Nil(t, s.LastSuccessfulWrite)
	require.False(t, a.Finish(nil))
	require.EqualValues(t, 1, a.Status().Invalid)
}

func TestQueuedIncidentTimeAndCumulativeLoss(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
	var clock atomic.Int64
	clock.Store(100)
	a := newAdapter(sink, Debug, bytes.NewReader(make([]byte, 16)), func() time.Time { return time.Unix(clock.Load(), 0) })
	a.Observe(Facts{Event: Startup})
	<-sink.entered
	clock.Store(200)
	a.Observe(Facts{Event: Readiness})
	for range QueueRecords {
		a.Observe(validCallStart(1))
	}
	before := a.Status()
	require.True(t, before.Writing)
	require.Equal(t, (QueueRecords-1)*RecordBytes, before.QueueBytes)
	require.Positive(t, before.Dropped)
	clock.Store(50) // Wall rollback cannot move the queued incident to delivery time.
	close(sink.release)
	require.True(t, a.Finish(nil))
	after := a.Status()
	require.Equal(t, before.Dropped, after.Dropped)
	require.Equal(t, before.Accepted, after.Written)
	require.NotNil(t, after.LastSuccessfulWrite)
	got := records(t, sink.buffer.Bytes())
	require.Equal(t, time.Unix(100, 0).UTC().Format(time.RFC3339), got[0]["time"])
	for _, record := range got {
		if record["event"] == "readiness" {
			require.Equal(t, time.Unix(200, 0).UTC().Format(time.RFC3339), record["time"])
		}
	}
}
