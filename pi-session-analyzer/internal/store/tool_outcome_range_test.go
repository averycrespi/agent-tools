package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestToolOutcomeRangeBoundsMultibyteResultContentByBytes(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "unicode", StartedAtUnix: &start, ToolCalls: []ingest.ToolCall{{ID: "c", Name: "mcp_call"}}, ToolResults: []ingest.ToolResult{{ID: "r", CallID: "c", Name: "mcp_call", Content: strings.Repeat("😀", 20000)}}}, SourceMeta{Path: "unicode", Size: 1}))
	report, err := s.ToolOutcomeRange(context.Background(), 100, 101)
	require.NoError(t, err)
	require.True(t, report.AnalysisContentTruncated)
	require.Equal(t, 1, report.Totals.Unknown)
}

func TestToolOutcomeRangeStopsBeforeReadingAnOversizedContentSet(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	calls := make([]ingest.ToolCall, 257)
	results := make([]ingest.ToolResult, len(calls))
	for i := range calls {
		id := fmt.Sprintf("c%03d", i)
		calls[i] = ingest.ToolCall{ID: id, Name: "bash"}
		results[i] = ingest.ToolResult{ID: "r" + id, CallID: id, Name: "bash", Content: strings.Repeat("x", toolOutcomeContentBytes)}
	}
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large-range", StartedAtUnix: &start, ToolCalls: calls, ToolResults: results}, SourceMeta{Path: "large-range", Size: 1}))
	report, err := s.ToolOutcomeRange(context.Background(), 100, 101)
	require.NoError(t, err)
	require.True(t, report.AnalysisTruncated)
	require.Zero(t, report.AnalyzedResults)
	require.Equal(t, len(calls), report.TotalCalls)
	require.Equal(t, ToolOutcomeTotals{Calls: len(calls), Unknown: len(calls)}, report.Totals)
}

func TestToolOutcomeRangeUsesTimedHalfOpenSessionsAndExactAssociation(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	truth, falsity := true, false
	starts := []*int64{int64Pointer(100), int64Pointer(199), int64Pointer(200), nil}
	for i, start := range starts {
		id := string(rune('a' + i))
		result := ingest.ToolResult{ID: "r", CallID: "c", Name: "bash", IsError: &falsity}
		if i == 1 {
			result.IsError = &truth
		}
		require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: id, StartedAtUnix: start, ToolCalls: []ingest.ToolCall{{ID: "c", Name: "bash"}}, ToolResults: []ingest.ToolResult{result}}, SourceMeta{Path: id + ".jsonl", Size: 1}))
	}
	report, err := s.ToolOutcomeRange(context.Background(), 100, 200)
	require.NoError(t, err)
	require.Equal(t, 2, report.TotalCalls)
	require.Equal(t, ToolOutcomeTotals{Calls: 2, ConfirmedErrors: 1, Successes: 1, Classifiable: 2}, report.Totals)
	require.Len(t, report.Tools, 1)
	require.Equal(t, "bash", report.Tools[0].Tool)
	require.InDelta(t, 0.5, *report.Tools[0].ErrorRate, 0.0001)
}

func int64Pointer(value int64) *int64 { return &value }
