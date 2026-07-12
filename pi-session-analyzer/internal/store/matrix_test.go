package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionMatrixBoundsLargeGoalStopAndCWDLabels(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	large := strings.Repeat("😀", 20000)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large", StartedAtUnix: &start, CWD: large, Messages: []ingest.Message{{ID: "m", Role: "assistant", StopReason: large}}, CustomStates: []ingest.CustomState{{ID: "g", Type: "goal-state", Status: large}}}, SourceMeta{Path: "large", Size: 1}))
	page, err := s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101}, nil)
	require.NoError(t, err)
	require.True(t, page.Rows[0].ContentTruncated)
	require.LessOrEqual(t, len(page.Rows[0].CWD), 64)
}

func TestSessionMatrixUsesHalfOpenRangeAndStableKeysetCursor(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	for _, session := range []ingest.Session{
		{ID: "a", StartedAtUnix: &start, CWD: "/work/a", Stats: ingest.ParseStats{Total: 4}, Messages: []ingest.Message{{ID: "m", Role: "assistant", OutputTokens: 3, StopReason: "stop", Cost: 1.25}}, Events: []ingest.Event{{ID: "e", Type: "compaction"}}},
		{ID: "b", StartedAtUnix: &start, CWD: "/work/b", Stats: ingest.ParseStats{Total: 2}},
		{ID: "untimed", CWD: "/work/u"},
	} {
		require.NoError(t, s.ReplaceSession(context.Background(), session, SourceMeta{Path: session.ID + ".jsonl", Size: 1, ModTimeNS: 1}))
	}
	require.NoError(t, s.SaveDetectorSuccess(context.Background(), "a", "retired", []DetectorFinding{{Detector: "retired", Classification: "structural", Severity: "error", Summary: "old", EvidenceID: "e", SourceLine: 1, Details: `{}`}}))
	first, err := s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101, Limit: 1}, []string{"d1", "d2"})
	require.NoError(t, err)
	require.Len(t, first.Rows, 1)
	require.Equal(t, "a", first.Rows[0].ID)
	require.Equal(t, 4, first.Rows[0].Records)
	require.Equal(t, 1, first.Rows[0].Turns)
	require.Equal(t, int64(3), first.Rows[0].OutputTokens)
	require.Equal(t, 1, first.Rows[0].Compactions)
	require.Equal(t, "stop", first.Rows[0].StopReason)
	require.Equal(t, 2, first.Rows[0].DetectorCoverage.NotRun)
	require.Equal(t, "none", first.Rows[0].FreshSeverity)
	require.NotEmpty(t, first.NextCursor)

	second, err := s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101, Limit: 1, Cursor: first.NextCursor}, []string{"d1", "d2"})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, []string{second.Rows[0].ID})
	require.Empty(t, second.NextCursor)

	empty, err := s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 99, ToUnix: 100, Limit: 10}, nil)
	require.NoError(t, err)
	require.Empty(t, empty.Rows)

	untimed, err := s.SessionMatrix(context.Background(), MatrixQuery{Untimed: true, Limit: 10}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"untimed"}, []string{untimed.Rows[0].ID})
}

func TestSessionMatrixBatchesToolOutcomeQueriesAcrossRows(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	flag := true
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{
			ID: id, StartedAtUnix: &start,
			ToolCalls:   []ingest.ToolCall{{ID: "call", Name: "bash"}},
			ToolResults: []ingest.ToolResult{{ID: "result", CallID: "call", Name: "bash", IsError: &flag}},
		}, SourceMeta{Path: id, Size: 1}))
	}
	toolQueries := 0
	reader := NewReader(
		func(ctx context.Context, query string, args ...any) (Rows, error) {
			if strings.Contains(query, "tool_results") {
				toolQueries++
			}
			return s.db.QueryContext(ctx, query, args...)
		},
		func(ctx context.Context, query string, args ...any) Row {
			if strings.Contains(query, "tool_results") {
				toolQueries++
			}
			return s.db.QueryRowContext(ctx, query, args...)
		},
	)
	page, err := reader.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101, Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, page.Rows, 3)
	require.LessOrEqual(t, toolQueries, 2)
	for _, row := range page.Rows {
		require.Equal(t, ToolOutcomeTotals{Calls: 1, ConfirmedErrors: 1, Classifiable: 1}, row.ToolOutcomes)
	}
}

func TestSessionMatrixRejectsCursorFromDifferentFilter(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "a", StartedAtUnix: &start}, SourceMeta{Path: "a", Size: 1}))
	page, err := s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101, Limit: 1}, nil)
	require.NoError(t, err)
	if page.NextCursor == "" {
		// Add a tie so the first query emits a cursor.
		require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "b", StartedAtUnix: &start}, SourceMeta{Path: "b", Size: 1}))
		page, err = s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 101, Limit: 1}, nil)
		require.NoError(t, err)
	}
	_, err = s.SessionMatrix(context.Background(), MatrixQuery{FromUnix: 100, ToUnix: 102, Limit: 1, Cursor: page.NextCursor}, nil)
	require.ErrorContains(t, err, "cursor does not match")
}
