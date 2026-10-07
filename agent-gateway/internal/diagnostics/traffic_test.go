package diagnostics

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficDiagnosticsDefaultLevelClosedAndRateBounded(t *testing.T) {
	var sink bytes.Buffer
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).UnixNano())
	adapter := newAdapter(&sink, Warn, strings.NewReader(strings.Repeat("a", 16)), func() time.Time { return time.Unix(0, clock.Load()) })
	for range 100 {
		adapter.Traffic(TrafficFacts(false, "locked", "begin", "not_started", 5))
	}
	adapter.Traffic(TrafficFacts(true, "locked", "begin", "not_started", 5))
	clock.Add(int64(time.Minute))
	adapter.Traffic(TrafficFacts(false, "io", "commit", "uncertain", 10))
	bad := TrafficFacts(false, "SECRET", "begin", "not_started", 5)
	adapter.Traffic(bad)
	bad = TrafficFacts(false, "locked", "begin", "not_started", 5)
	bad.SQLiteCode = 65536
	adapter.Traffic(bad)
	require.True(t, adapter.Finish(nil))
	<-adapter.Done()
	output := sink.String()
	require.Equal(t, 2, strings.Count(output, `"event":"traffic_failure"`))
	require.Equal(t, 1, strings.Count(output, `"event":"traffic_recovered"`))
	require.Contains(t, output, `"settlement":"uncertain"`)
	require.Contains(t, output, `"action":"inspect_settlement_no_replay"`)
	require.NotContains(t, output, "SECRET")
	require.EqualValues(t, 2, adapter.Status().Invalid)
}
