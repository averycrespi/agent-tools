package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestOverviewSignalSummaryPreservesGoalStopAndDistributionStates(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	sessions := []ingest.Session{
		{ID: "absent", StartedAtUnix: &start, Stats: ingest.ParseStats{Total: 0}},
		{ID: "active", StartedAtUnix: &start, Stats: ingest.ParseStats{Total: 25}, CustomStates: []ingest.CustomState{{ID: "g", Type: "goal-state", Status: "active"}}, Messages: []ingest.Message{{ID: "m", Role: "assistant", StopReason: "tool_use"}}},
		{ID: "cleared", StartedAtUnix: &start, Stats: ingest.ParseStats{Total: 101}, CustomStates: []ingest.CustomState{{ID: "g", Type: "goal-state", Status: ""}}},
	}
	for _, session := range sessions {
		require.NoError(t, s.ReplaceSession(context.Background(), session, SourceMeta{Path: session.ID, Size: 1}))
	}
	summary, err := s.OverviewSignalSummary(context.Background(), 100, 101)
	require.NoError(t, err)
	require.ElementsMatch(t, []CategoryCount{{Value: "absent", Count: 1}, {Value: "active", Count: 1}, {Value: "cleared", Count: 1}}, summary.Goals)
	require.ElementsMatch(t, []CategoryCount{{Value: "absent", Count: 2}, {Value: "tool_use", Count: 1}}, summary.Stops)
	require.Equal(t, 1, summary.Records[0].Count)
	require.Equal(t, 1, summary.Records[2].Count)
	require.Equal(t, 1, summary.Records[4].Count)
	require.Equal(t, 2, summary.Turns[0].Count)
	require.Equal(t, 1, summary.Turns[1].Count)
	overview, err := s.Overview(context.Background(), OverviewQuery{Timezone: "UTC", Unit: BucketDay, Buckets: []CalendarBucket{{Key: "one", StartUnix: 100, EndUnix: 101}}})
	require.NoError(t, err)
	require.Len(t, overview.StopReasons, 2)
	for _, series := range overview.StopReasons {
		if series.Value == "absent" {
			require.Equal(t, []int{2}, series.Counts)
		}
		if series.Value == "tool_use" {
			require.Equal(t, []int{1}, series.Counts)
		}
	}
}
