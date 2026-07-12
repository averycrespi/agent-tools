package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestGoalDiagnosticsPreservesAbsentClearedAndProgression(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "absent"}, SourceMeta{Path: "absent", Size: 1}))
	absent, err := s.GoalDiagnosticsPage(context.Background(), "absent", 0, 10)
	require.NoError(t, err)
	require.Equal(t, "absent", absent.FinalState)

	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "goals", CustomStates: []ingest.CustomState{
		{ID: "g1", SourceLine: 2, Type: "goal-state", Status: "active"},
		{ID: "g2", SourceLine: 4, Type: "goal-state", Status: ""},
		{ID: "g3", SourceLine: 6, Type: "goal-state", Status: "complete"},
	}}, SourceMeta{Path: "goals", Size: 1}))
	first, err := s.GoalDiagnosticsPage(context.Background(), "goals", 0, 2)
	require.NoError(t, err)
	require.Equal(t, "complete", first.FinalState)
	require.Equal(t, 3, first.Total)
	require.Equal(t, []string{"active", "cleared"}, []string{first.Snapshots[0].State, first.Snapshots[1].State})
	require.True(t, first.Truncated)
	second, err := s.GoalDiagnosticsPage(context.Background(), "goals", 2, 2)
	require.NoError(t, err)
	require.Equal(t, "complete", second.Snapshots[0].State)
}
