package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/outcome"
)

type ToolOutcomeTotals struct {
	Calls           int `json:"calls"`
	ConfirmedErrors int `json:"confirmed_errors"`
	InferredErrors  int `json:"inferred_errors"`
	Successes       int `json:"successes"`
	Unknown         int `json:"unknown"`
	Classifiable    int `json:"classifiable"`
}

type ToolOutcomeDataQuality struct {
	OrphanResults   int `json:"orphan_results"`
	MultipleResults int `json:"multiple_results"`
	NameMismatches  int `json:"name_mismatches"`
}

type ToolOutcomeRow struct {
	ToolID    string            `json:"tool_id"`
	Tool      string            `json:"tool"`
	Totals    ToolOutcomeTotals `json:"totals"`
	ErrorRate *float64          `json:"error_rate"`
}

type ToolOutcomeReport struct {
	Totals                   ToolOutcomeTotals      `json:"totals"`
	Tools                    []ToolOutcomeRow       `json:"tools"`
	ToolsTruncated           bool                   `json:"tools_truncated"`
	ContentTruncated         bool                   `json:"content_truncated"`
	AnalysisContentTruncated bool                   `json:"analysis_content_truncated"`
	DataQuality              ToolOutcomeDataQuality `json:"data_quality"`
	TotalCalls               int                    `json:"total_calls"`
	AnalyzedCalls            int                    `json:"analyzed_calls"`
	TotalResults             int                    `json:"total_results"`
	AnalyzedResults          int                    `json:"analyzed_results"`
	AnalysisTruncated        bool                   `json:"analysis_truncated"`
}

type outcomeCall struct {
	name        string
	results     int
	accumulator outcome.Accumulator
}

const (
	maxAnalyzedCalls            = 5000
	maxAnalyzedResults          = 20000
	toolOutcomeContentBytes     = 32768
	maxToolAnalysisContentBytes = 8 * 1024 * 1024
)

func (s *Reader) ToolOutcomeReport(ctx context.Context, prefix string) (ToolOutcomeReport, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return ToolOutcomeReport{}, err
	}
	reports, err := s.toolOutcomeReports(ctx, []string{id})
	if err != nil {
		return ToolOutcomeReport{}, err
	}
	return reports[id], nil
}

func (s *Reader) toolOutcomeReports(ctx context.Context, sessionIDs []string) (map[string]ToolOutcomeReport, error) {
	reports := make(map[string]ToolOutcomeReport, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return reports, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sessionIDs)), ",")
	args := make([]any, len(sessionIDs))
	for i, id := range sessionIDs {
		args[i] = id
		reports[id] = ToolOutcomeReport{Tools: []ToolOutcomeRow{}}
	}
	countArgs := []any{toolOutcomeContentBytes}
	countArgs = append(countArgs, args...)
	rows, err := s.query.QueryContext(ctx, `SELECT s.id,
 (SELECT COUNT(*) FROM tool_calls c WHERE c.session_id=s.id),
 (SELECT COUNT(*) FROM tool_results r WHERE r.session_id=s.id),
 (SELECT COALESCE(SUM(MIN(length(CAST(r.content AS BLOB)),?)),0) FROM tool_results r WHERE r.session_id=s.id AND r.is_error IS NULL)
 FROM sessions s WHERE s.id IN (`+placeholders+`) ORDER BY s.id`, countArgs...)
	if err != nil {
		return nil, fmt.Errorf("count tool outcomes: %w", err)
	}
	eligible := make([]string, 0, len(sessionIDs))
	for rows.Next() {
		var id string
		var analysisContentBytes int64
		report := ToolOutcomeReport{Tools: []ToolOutcomeRow{}}
		if err = rows.Scan(&id, &report.TotalCalls, &report.TotalResults, &analysisContentBytes); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan tool outcome counts: %w", err)
		}
		report.AnalysisTruncated = report.TotalCalls > maxAnalyzedCalls || report.TotalResults > maxAnalyzedResults || analysisContentBytes > maxToolAnalysisContentBytes
		if !report.AnalysisTruncated {
			eligible = append(eligible, id)
		}
		reports[id] = report
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read tool outcome counts: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("close tool outcome counts: %w", err)
	}
	if len(eligible) == 0 {
		return reports, nil
	}
	eligiblePlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(eligible)), ",")
	eligibleArgs := make([]any, len(eligible))
	calls := make(map[string]map[string]*outcomeCall, len(eligible))
	for i, id := range eligible {
		eligibleArgs[i] = id
		calls[id] = map[string]*outcomeCall{}
	}
	rows, err = s.query.QueryContext(ctx, `SELECT session_id,id,name FROM tool_calls WHERE session_id IN (`+eligiblePlaceholders+`) ORDER BY session_id,id`, eligibleArgs...)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	for rows.Next() {
		var sessionID, callID, name string
		if err = rows.Scan(&sessionID, &callID, &name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		calls[sessionID][callID] = &outcomeCall{name: name}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read tool calls: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("close tool calls: %w", err)
	}
	for id, sessionCalls := range calls {
		report := reports[id]
		report.AnalyzedCalls = len(sessionCalls)
		reports[id] = report
	}
	resultArgs := []any{toolOutcomeContentBytes, toolOutcomeContentBytes}
	resultArgs = append(resultArgs, eligibleArgs...)
	rows, err = s.query.QueryContext(ctx, `SELECT session_id,call_id,name,
 CASE WHEN is_error IS NULL THEN COALESCE(substr(CAST(content AS BLOB),1,?),X'') ELSE X'' END,
 is_error,
 is_error IS NULL AND length(CAST(content AS BLOB))>? FROM tool_results WHERE session_id IN (`+eligiblePlaceholders+`) ORDER BY session_id,id`, resultArgs...)
	if err != nil {
		return nil, fmt.Errorf("query tool results: %w", err)
	}
	for rows.Next() {
		var sessionID, callID, name, content string
		var flag sql.NullBool
		var contentTruncated bool
		if err = rows.Scan(&sessionID, &callID, &name, &content, &flag, &contentTruncated); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan tool result: %w", err)
		}
		report := reports[sessionID]
		report.AnalyzedResults++
		report.AnalysisContentTruncated = report.AnalysisContentTruncated || contentTruncated
		call, exists := calls[sessionID][callID]
		if !exists {
			report.DataQuality.OrphanResults++
			reports[sessionID] = report
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
		reports[sessionID] = report
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read tool results: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, fmt.Errorf("close tool results: %w", err)
	}
	for id, sessionCalls := range calls {
		report := reports[id]
		finalizeToolOutcomeReport(&report, sessionCalls)
		reports[id] = report
	}
	return reports, nil
}

func finalizeToolOutcomeReport(report *ToolOutcomeReport, calls map[string]*outcomeCall) {
	byTool := map[string]ToolOutcomeTotals{}
	for _, call := range calls {
		if call.results > 1 {
			report.DataQuality.MultipleResults++
		}
		totals := byTool[call.name]
		classification := call.accumulator.Classification()
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
}

func addOutcome(totals *ToolOutcomeTotals, classification outcome.Classification) {
	totals.Calls++
	switch classification {
	case outcome.ConfirmedError:
		totals.ConfirmedErrors++
		totals.Classifiable++
	case outcome.InferredError:
		totals.InferredErrors++
		totals.Classifiable++
	case outcome.Success:
		totals.Successes++
		totals.Classifiable++
	default:
		totals.Unknown++
	}
}
