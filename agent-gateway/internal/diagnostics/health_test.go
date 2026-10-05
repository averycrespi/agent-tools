package diagnostics

import (
	"bytes"
	"errors"
	"sync"
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

type blockedFailedHealthSink struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Uint64
}

func (s *blockedFailedHealthSink) Write(data []byte) (int, error) {
	s.calls.Add(1)
	close(s.entered)
	<-s.release
	return len(data) / 2, errors.New("secret-canary")
}

func TestFailedWorkerAccountsAbandonedAndLaterRecords(t *testing.T) {
	sink := &blockedFailedHealthSink{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(sink.release) }) }
	t.Cleanup(unblock)
	a := New(sink, Debug)
	a.Observe(Facts{Event: Startup})
	<-sink.entered
	for range 3 {
		a.Observe(Facts{Event: Readiness})
	}
	require.EqualValues(t, 4, a.Status().Accepted)
	require.Equal(t, 3, a.Status().QueueRecords)
	unblock()
	<-a.Done()

	s := a.Status()
	require.Equal(t, "failed", s.State)
	require.EqualValues(t, 4, s.Accepted)
	// The partial write is uncertain, not a definitely unattempted drop.
	require.EqualValues(t, 3, s.Dropped)
	require.EqualValues(t, 1, s.WriteFailures)
	require.Zero(t, s.Written)
	require.Zero(t, s.QueueRecords)
	require.Zero(t, s.QueueBytes)
	require.False(t, s.Writing)
	require.Nil(t, s.LastSuccessfulWrite)

	var producers sync.WaitGroup
	for range 20 {
		producers.Go(func() { a.Observe(Facts{Event: Readiness}) })
	}
	producers.Wait()
	require.EqualValues(t, 4, a.Status().Accepted)
	require.EqualValues(t, 23, a.Status().Dropped)
	require.EqualValues(t, 1, sink.calls.Load())
	require.False(t, a.Finish(nil))
	require.EqualValues(t, 23, a.Status().Dropped)
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
