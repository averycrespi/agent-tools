package invocation

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedClock struct {
	wall time.Time
	tick time.Duration
}

func (c *recordedClock) read() (time.Time, time.Duration) { return c.wall, c.tick }
func (c *recordedClock) advance(d time.Duration)          { c.wall = c.wall.Add(d); c.tick += d }
func recordedFixture() (*recordedActivity, *recordedClock) {
	clock := &recordedClock{wall: time.Date(2026, 9, 30, 12, 0, 30, 0, time.UTC)}
	return newRecordedActivityClock(clock.read), clock
}

func TestRecordedActivityCoverageAndIndependentSettlementTimes(t *testing.T) {
	ring, clock := recordedFixture()
	initial := ring.snapshot()
	require.Equal(t, "unavailable", initial.Coverage)
	require.Len(t, initial.Buckets, 15)
	for _, bucket := range initial.Buckets {
		require.Nil(t, bucket.Counts)
		require.Nil(t, bucket.ObservedStart)
	}
	ring.record(recordedEvent{recordedMCP, recordedAllow})
	clock.advance(time.Minute)
	ring.record(recordedEvent{recordedMCP, recordedDownstreamFailure})
	first := ring.snapshot()
	require.Equal(t, "partial", first.Coverage)
	require.Equal(t, "partial", first.Buckets[14].Coverage)
	assert.Equal(t, initial.CollectionStart, *first.Buckets[14].ObservedStart)
	assert.Equal(t, uint64(1), first.Buckets[14].Counts.MCP.Admissions.Allow)
	assert.Zero(t, first.Buckets[14].Counts.MCP.Completions.DownstreamFailure)
	clock.advance(time.Minute)
	second := ring.snapshot()
	assert.Equal(t, uint64(1), second.Buckets[13].Counts.MCP.Admissions.Allow)
	assert.Equal(t, uint64(1), second.Buckets[14].Counts.MCP.Completions.DownstreamFailure)
	// Returned snapshots own their values; mutation cannot alter ring evidence.
	second.Buckets[13].Counts.MCP.Admissions.Allow = 999
	assert.Equal(t, uint64(1), ring.snapshot().Buckets[13].Counts.MCP.Admissions.Allow)
	clock.advance(15 * time.Minute)
	complete := ring.snapshot()
	assert.Equal(t, "complete", complete.Coverage)
	for _, bucket := range complete.Buckets {
		require.NotNil(t, bucket.Counts)
		assert.Equal(t, contract.RecordedProtocols{}, *bucket.Counts)
	}
	clock.advance(2 * time.Hour)
	ring.record(recordedEvent{recordedConnect, recordedInterception})
	clock.advance(time.Minute)
	assert.Equal(t, uint64(1), ring.snapshot().Buckets[14].Counts.Connect.Admissions.InterceptionSelected)
}

func TestRecordedActivityBucketsBeforeUnixEpoch(t *testing.T) {
	clock := &recordedClock{wall: time.Date(1969, 12, 31, 23, 59, 30, 0, time.UTC)}
	ring := newRecordedActivityClock(clock.read)
	ring.record(recordedEvent{recordedMCP, recordedAllow})
	clock.advance(time.Minute)
	snapshot := ring.snapshot()
	require.Equal(t, "partial", snapshot.Buckets[14].Coverage)
	require.NotNil(t, snapshot.Buckets[14].Counts)
	assert.Equal(t, uint64(1), snapshot.Buckets[14].Counts.MCP.Admissions.Allow)
}

func TestRecordedActivityResetOverflowAndRestart(t *testing.T) {
	for _, reset := range []string{"backward", "forward", "overflow"} {
		t.Run(reset, func(t *testing.T) {
			ring, clock := recordedFixture()
			ring.record(recordedEvent{recordedMCP, recordedAllow})
			before := ring.snapshot()
			reason := "clock_reset"
			switch reset {
			case "backward":
				clock.wall = clock.wall.Add(-time.Minute)
			case "forward":
				clock.wall = clock.wall.Add(time.Hour)
			case "overflow":
				reason = "counter_overflow"
				minute := clock.wall.Unix() / 60
				ring.buckets[minute%60].counts.MCP.Admissions.Allow = contract.RecordedActivityMaxCount
				ring.record(recordedEvent{recordedMCP, recordedAllow})
			}
			after := ring.snapshot()
			assert.NotEqual(t, before.Epoch, after.Epoch)
			assert.Equal(t, reason, after.EpochReason)
			assert.Equal(t, "unavailable", after.Coverage)
			for _, bucket := range after.Buckets {
				assert.Nil(t, bucket.Counts)
			}
			clock.advance(2 * time.Minute)
			assert.Equal(t, "partial", ring.snapshot().Coverage)
		})
	}
	first, _ := recordedFixture()
	second, _ := recordedFixture()
	assert.NotEqual(t, first.snapshot().Epoch, second.snapshot().Epoch)
}

func TestRecordedActivityConcurrentSnapshotAndClosedPrivacy(t *testing.T) {
	ring, clock := recordedFixture()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				ring.record(recordedEvent{recordedHTTPRequest, recordedOutcomeUnknown})
				assert.Len(t, ring.snapshot().Buckets, 15)
			}
		})
	}
	group.Wait()
	clock.advance(time.Minute)
	snapshot := ring.snapshot()
	require.Equal(t, uint64(800), snapshot.Buckets[14].Counts.HTTPRequest.Completions.OutcomeUnknown)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	for _, forbidden := range []string{"principal", "credential", "target", "destination", "arguments", "body", "path", "status_code"} {
		assert.NotContains(t, string(encoded), `"`+forbidden+`"`)
	}
	assert.Less(t, len(encoded), 64*1024)
	assert.Len(t, ring.buckets, contract.RecordedActivityBuckets)
}
