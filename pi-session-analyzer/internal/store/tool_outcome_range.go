package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/outcome"
)

const (
	maxRangeAnalyzedCalls   = 200000
	maxRangeAnalyzedResults = 400000
)

type rangeCallKey struct {
	sessionID string
	callID    string
}

func (s *Reader) ToolOutcomeRange(ctx context.Context, fromUnix, toUnix int64) (ToolOutcomeReport, error) {
	if fromUnix >= toUnix {
		return ToolOutcomeReport{}, fmt.Errorf("range start must be before range end")
	}
	report := ToolOutcomeReport{Tools: []ToolOutcomeRow{}}
	var analysisContentBytes int64
	if err := s.query.QueryRowContext(ctx, `
SELECT
 (SELECT COUNT(*) FROM tool_calls c JOIN sessions s ON s.id=c.session_id WHERE s.started_at_unix>=? AND s.started_at_unix<?),
 (SELECT COUNT(*) FROM tool_results r JOIN sessions s ON s.id=r.session_id WHERE s.started_at_unix>=? AND s.started_at_unix<?),
 (SELECT COALESCE(SUM(MIN(length(CAST(r.content AS BLOB)),?)),0) FROM tool_results r JOIN sessions s ON s.id=r.session_id WHERE r.is_error IS NULL AND s.started_at_unix>=? AND s.started_at_unix<?)`,
		fromUnix, toUnix, fromUnix, toUnix, toolOutcomeContentBytes, fromUnix, toUnix).Scan(&report.TotalCalls, &report.TotalResults, &analysisContentBytes); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("count ranged tool outcomes: %w", err)
	}
	report.AnalysisTruncated = report.TotalCalls > maxRangeAnalyzedCalls || report.TotalResults > maxRangeAnalyzedResults || analysisContentBytes > maxToolAnalysisContentBytes
	if report.AnalysisTruncated {
		report.Totals = ToolOutcomeTotals{Calls: report.TotalCalls, Unknown: report.TotalCalls}
		return report, nil
	}
	calls := make(map[rangeCallKey]*outcomeCall, report.TotalCalls)
	rows, err := s.query.QueryContext(ctx, `
SELECT c.session_id,c.id,c.name FROM tool_calls c JOIN sessions s ON s.id=c.session_id
WHERE s.started_at_unix>=? AND s.started_at_unix<? ORDER BY c.session_id,c.id`, fromUnix, toUnix)
	if err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("query ranged tool calls: %w", err)
	}
	for rows.Next() {
		var sessionID, callID, name string
		if err = rows.Scan(&sessionID, &callID, &name); err != nil {
			_ = rows.Close()
			return ToolOutcomeReport{}, fmt.Errorf("scan ranged tool call: %w", err)
		}
		calls[rangeCallKey{sessionID: sessionID, callID: callID}] = &outcomeCall{name: name}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return ToolOutcomeReport{}, fmt.Errorf("read ranged tool calls: %w", err)
	}
	if err = rows.Close(); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("close ranged tool calls: %w", err)
	}
	report.AnalyzedCalls = len(calls)
	rows, err = s.query.QueryContext(ctx, `
SELECT r.session_id,r.call_id,r.name,
 CASE WHEN r.is_error IS NULL THEN COALESCE(substr(CAST(r.content AS BLOB),1,?),X'') ELSE X'' END,
 r.is_error,
 r.is_error IS NULL AND length(CAST(r.content AS BLOB))>? FROM tool_results r JOIN sessions s ON s.id=r.session_id
WHERE s.started_at_unix>=? AND s.started_at_unix<? ORDER BY r.session_id,r.id`, toolOutcomeContentBytes, toolOutcomeContentBytes, fromUnix, toUnix)
	if err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("query ranged tool results: %w", err)
	}
	for rows.Next() {
		var sessionID, callID, name, content string
		var flag sql.NullBool
		var contentTruncated bool
		if err = rows.Scan(&sessionID, &callID, &name, &content, &flag, &contentTruncated); err != nil {
			_ = rows.Close()
			return ToolOutcomeReport{}, fmt.Errorf("scan ranged tool result: %w", err)
		}
		report.AnalysisContentTruncated = report.AnalysisContentTruncated || contentTruncated
		call, exists := calls[rangeCallKey{sessionID: sessionID, callID: callID}]
		if !exists {
			report.DataQuality.OrphanResults++
			continue
		}
		var isError *bool
		if flag.Valid {
			isError = &flag.Bool
		}
		call.results++
		call.accumulator.Observe(call.name, outcome.Result{Name: name, Content: content, IsError: isError})
		if name != "" && name != call.name {
			report.DataQuality.NameMismatches++
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return ToolOutcomeReport{}, fmt.Errorf("read ranged tool results: %w", err)
	}
	if err = rows.Close(); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("close ranged tool results: %w", err)
	}
	report.AnalyzedResults = report.TotalResults
	byTool := map[string]ToolOutcomeTotals{}
	for _, call := range calls {
		if call.results > 1 {
			report.DataQuality.MultipleResults++
		}
		classification := call.accumulator.Classification()
		totals := byTool[call.name]
		addOutcome(&totals, classification)
		byTool[call.name] = totals
		addOutcome(&report.Totals, classification)
	}
	names := make([]string, 0, len(byTool))
	for name := range byTool {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 50 {
		names = names[:50]
		report.ToolsTruncated = true
	}
	for _, name := range names {
		totals := byTool[name]
		boundedName := truncateUTF8Bytes(name, 64)
		report.ContentTruncated = report.ContentTruncated || boundedName != name
		digest := sha256.Sum256([]byte(name))
		row := ToolOutcomeRow{ToolID: fmt.Sprintf("%x", digest[:8]), Tool: boundedName, Totals: totals}
		if totals.Classifiable > 0 {
			rate := float64(totals.ConfirmedErrors+totals.InferredErrors) / float64(totals.Classifiable)
			row.ErrorRate = &rate
		}
		report.Tools = append(report.Tools, row)
	}
	return report, nil
}
