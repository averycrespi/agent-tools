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

func TestToolOutcomeReportReturnsNoMisleadingPartialRatesPastAnalysisBound(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	calls := make([]ingest.ToolCall, maxAnalyzedCalls+1)
	for i := range calls {
		calls[i] = ingest.ToolCall{ID: fmt.Sprintf("c%05d", i), Name: "bash", SourceLine: i + 1}
	}
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large", ToolCalls: calls}, SourceMeta{Path: "large.jsonl", Size: 1, ModTimeNS: 1}))
	report, err := s.ToolOutcomeReport(context.Background(), "large")
	require.NoError(t, err)
	require.True(t, report.AnalysisTruncated)
	require.Equal(t, maxAnalyzedCalls+1, report.TotalCalls)
	require.Zero(t, report.AnalyzedCalls)
	require.Zero(t, report.Totals.Calls)
	require.Empty(t, report.Tools)
}

func TestToolOutcomeReportStopsBeforeOversizedContentAnalysis(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	calls := make([]ingest.ToolCall, 257)
	results := make([]ingest.ToolResult, len(calls))
	for i := range calls {
		id := fmt.Sprintf("c%03d", i)
		calls[i] = ingest.ToolCall{ID: id, Name: "bash"}
		results[i] = ingest.ToolResult{ID: "r" + id, CallID: id, Name: "bash", Content: strings.Repeat("x", toolOutcomeContentBytes)}
	}
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large-content", ToolCalls: calls, ToolResults: results}, SourceMeta{Path: "large-content", Size: 1}))
	report, err := s.ToolOutcomeReport(context.Background(), "large-content")
	require.NoError(t, err)
	require.True(t, report.AnalysisTruncated)
	require.Zero(t, report.AnalyzedCalls)
	require.Zero(t, report.AnalyzedResults)
}

func TestToolOutcomeReportClassifiesEachExactCallOnceWithCoverage(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	truth, falsity := true, false
	session := ingest.Session{
		ID: "outcomes",
		ToolCalls: []ingest.ToolCall{
			{ID: "c1", Name: "bash"}, {ID: "c2", Name: "mcp_call"}, {ID: "c3", Name: "bash"},
			{ID: "c4", Name: "bash"}, {ID: "c5", Name: "read"},
		},
		ToolResults: []ingest.ToolResult{
			{ID: "r1", CallID: "c1", Name: "bash", IsError: &falsity},
			{ID: "r2", CallID: "c1", Name: "wrong", IsError: &truth},
			{ID: "r3", CallID: "c2", Name: "mcp_call", Content: "fetch failed while connecting"},
			{ID: "r4", CallID: "c3", Name: "bash", IsError: &falsity},
			{ID: "r5", CallID: "c5", Name: "read", Content: "fetch failed"},
			{ID: "orphan", CallID: "missing", Name: "bash", IsError: &truth},
		},
	}
	require.NoError(t, s.ReplaceSession(context.Background(), session, SourceMeta{Path: "outcomes.jsonl", Size: 1, ModTimeNS: 1}))

	report, err := s.ToolOutcomeReport(context.Background(), "out")
	require.NoError(t, err)
	require.Equal(t, ToolOutcomeDataQuality{OrphanResults: 1, MultipleResults: 1, NameMismatches: 1}, report.DataQuality)
	require.Equal(t, ToolOutcomeTotals{Calls: 5, ConfirmedErrors: 1, InferredErrors: 1, Successes: 1, Unknown: 2, Classifiable: 3}, report.Totals)
	require.Len(t, report.Tools, 3)
	require.Equal(t, "bash", report.Tools[0].Tool)
	require.Equal(t, ToolOutcomeTotals{Calls: 3, ConfirmedErrors: 1, Successes: 1, Unknown: 1, Classifiable: 2}, report.Tools[0].Totals)
	require.NotNil(t, report.Tools[0].ErrorRate)
	require.InDelta(t, 0.5, *report.Tools[0].ErrorRate, 0.0001)
	require.Equal(t, "mcp_call", report.Tools[1].Tool)
	require.InDelta(t, 1.0, *report.Tools[1].ErrorRate, 0.0001)
	require.Equal(t, "read", report.Tools[2].Tool)
	require.Nil(t, report.Tools[2].ErrorRate)
}
