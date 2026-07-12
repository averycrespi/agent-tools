package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestDetectorOverviewScopesFreshnessAndCoverageToCurrentRegistry(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	for _, id := range []string{"a", "b"} {
		require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: id, StartedAtUnix: &start}, SourceMeta{Path: id, Size: 1}))
	}
	finding := func(detector, severity string) DetectorFinding {
		return DetectorFinding{Detector: detector, Classification: "structural", Severity: severity, Summary: detector, EvidenceID: detector, SourceLine: 1, Details: `{}`}
	}
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "a", "d1", []DetectorFinding{finding("d1", "warn")}))
	require.NoError(t, s.SaveDetectorFailure(context.Background(), "a", "d2", errors.New("failed")))
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "a", "retired", []DetectorFinding{finding("retired", "error")}))

	rows, err := s.DetectorOverview(context.Background(), 100, 101, []string{"d1", "d2"})
	require.NoError(t, err)
	require.Equal(t, []DetectorOverviewRow{
		{Detector: "d1", Fresh: FreshFindingCounts{Warn: 1, Structural: 1}, Coverage: DetectorCoverage{Success: 1, NotRun: 1}},
		{Detector: "d2", Coverage: DetectorCoverage{Failed: 1, NotRun: 1}},
	}, rows)
	overview, err := s.Overview(context.Background(), OverviewQuery{Timezone: "UTC", Unit: BucketDay, DetectorNames: []string{"d1", "d2"}, Buckets: []CalendarBucket{{Key: "bucket", StartUnix: 100, EndUnix: 101}}})
	require.NoError(t, err)
	require.Equal(t, FreshFindingCounts{Warn: 1, Structural: 1}, overview.Buckets[0].FreshFindings)
	require.Equal(t, DetectorCoverage{Success: 1, Failed: 1, NotRun: 2}, overview.Buckets[0].DetectorCoverage)
	require.Equal(t, GoalOutcomeCounts{Absent: 2}, overview.Buckets[0].GoalOutcomes)
}
