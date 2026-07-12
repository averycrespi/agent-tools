package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestCalendarBucketsUseTimezoneCalendarBoundariesAcrossDST(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	from := time.Date(2026, 3, 7, 0, 0, 0, 0, location)
	to := time.Date(2026, 3, 10, 0, 0, 0, 0, location)
	buckets, err := CalendarBuckets(from, to, time.Date(2026, 3, 9, 12, 0, 0, 0, location), location, BucketDay)
	require.NoError(t, err)
	require.Len(t, buckets, 3)
	require.Equal(t, int64(24*60*60), buckets[0].EndUnix-buckets[0].StartUnix)
	require.Equal(t, int64(23*60*60), buckets[1].EndUnix-buckets[1].StartUnix)
	require.Equal(t, int64(24*60*60), buckets[2].EndUnix-buckets[2].StartUnix)
	require.False(t, buckets[0].Partial)
	require.False(t, buckets[1].Partial)
	require.True(t, buckets[2].Partial)
}

func TestCalendarWeekBucketsUseMondayBoundaries(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	buckets, err := CalendarBuckets(from, to, from, time.UTC, BucketWeek)
	require.NoError(t, err)
	require.Len(t, buckets, 3)
	require.Equal(t, "2026-W02", buckets[0].Key)
	require.Equal(t, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC).Unix(), buckets[0].EndUnix)
	require.Equal(t, "2026-W03", buckets[1].Key)
	require.Equal(t, time.Date(2026, 1, 19, 0, 0, 0, 0, time.UTC).Unix(), buckets[1].EndUnix)
	require.Equal(t, "2026-W04", buckets[2].Key)
}

func TestCalendarBucketsValidateRangeUnitAndBound(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := CalendarBuckets(from, from, from, time.UTC, BucketDay)
	require.ErrorContains(t, err, "before")
	_, err = CalendarBuckets(from, from.AddDate(0, 0, 1), from, time.UTC, BucketUnit("hour"))
	require.ErrorContains(t, err, "bucket")
	_, err = CalendarBuckets(from, from.AddDate(0, 0, MaxOverviewBuckets+1), from, time.UTC, BucketDay)
	require.ErrorContains(t, err, "too many")
}

func TestOverviewReturnsEmptyBucketsAndDedupeSafeSplitMetrics(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	jan1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	jan2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	jan4 := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC).Unix()
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{
		ID: "first", StartedAtUnix: &jan1,
		Messages: []ingest.Message{
			{ID: "m1", Role: "assistant", Cost: 0.1, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 7},
			{ID: "m2", Role: "user", Cost: 0.2, OutputTokens: 1},
		},
		ToolCalls:      []ingest.ToolCall{{ID: "c1", Name: "bash"}, {ID: "c2", Name: "read"}},
		Events:         []ingest.Event{{ID: "e1", Type: "compaction"}},
		CustomMessages: []ingest.CustomMessage{{ID: "g1", Type: "broker-guard"}},
	}, SourceMeta{Path: "first.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "second", StartedAtUnix: &jan2}, SourceMeta{Path: "second.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "excluded", StartedAtUnix: &jan4}, SourceMeta{Path: "excluded.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "untimed"}, SourceMeta{Path: "untimed.jsonl", Size: 1, ModTimeNS: 1}))

	from := time.Unix(jan1, 0).UTC()
	to := time.Unix(jan4, 0).UTC()
	buckets, err := CalendarBuckets(from, to, from, time.UTC, BucketDay)
	require.NoError(t, err)
	overview, err := s.Overview(context.Background(), OverviewQuery{Timezone: "UTC", Unit: BucketDay, Buckets: buckets})
	require.NoError(t, err)
	require.Equal(t, 1, overview.UntimedSessions)
	require.Len(t, overview.Buckets, 3)
	require.Equal(t, 1, overview.Buckets[0].Sessions)
	require.InDelta(t, 0.3, overview.Buckets[0].Cost, 0.00001)
	require.Equal(t, int64(3), overview.Buckets[0].OutputTokens)
	require.Equal(t, int64(3), overview.Buckets[0].ReasoningTokens)
	require.Equal(t, int64(5), overview.Buckets[0].CacheReadTokens)
	require.Equal(t, int64(7), overview.Buckets[0].CacheWriteTokens)
	require.Equal(t, 2, overview.Buckets[0].ToolCalls)
	require.Equal(t, 1, overview.Buckets[0].Compactions)
	require.Equal(t, 1, overview.Buckets[0].BrokerGuards)
	require.Equal(t, 2, overview.ToolOutcomes.TotalCalls)
	require.Equal(t, 2, overview.ToolOutcomes.Totals.Unknown)
	require.Nil(t, overview.ToolOutcomes.Tools[0].ErrorRate)
	require.Equal(t, 1, overview.Buckets[1].Sessions)
	require.Zero(t, overview.Buckets[2].Sessions)
}
