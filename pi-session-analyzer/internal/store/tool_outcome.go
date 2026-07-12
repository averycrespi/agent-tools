package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"

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
	Totals            ToolOutcomeTotals      `json:"totals"`
	Tools             []ToolOutcomeRow       `json:"tools"`
	ToolsTruncated    bool                   `json:"tools_truncated"`
	ContentTruncated  bool                   `json:"content_truncated"`
	DataQuality       ToolOutcomeDataQuality `json:"data_quality"`
	TotalCalls        int                    `json:"total_calls"`
	AnalyzedCalls     int                    `json:"analyzed_calls"`
	TotalResults      int                    `json:"total_results"`
	AnalyzedResults   int                    `json:"analyzed_results"`
	AnalysisTruncated bool                   `json:"analysis_truncated"`
}

type outcomeCall struct {
	name        string
	results     int
	accumulator outcome.Accumulator
}

const (
	maxAnalyzedCalls   = 5000
	maxAnalyzedResults = 20000
)

func (s *Reader) ToolOutcomeReport(ctx context.Context, prefix string) (ToolOutcomeReport, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return ToolOutcomeReport{}, err
	}
	report := ToolOutcomeReport{Tools: []ToolOutcomeRow{}}
	if err = s.query.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tool_calls WHERE session_id=?),(SELECT COUNT(*) FROM tool_results WHERE session_id=?)`, id, id).Scan(&report.TotalCalls, &report.TotalResults); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("count tool outcomes: %w", err)
	}
	report.AnalysisTruncated = report.TotalCalls > maxAnalyzedCalls || report.TotalResults > maxAnalyzedResults
	if report.AnalysisTruncated {
		return report, nil
	}
	calls := map[string]*outcomeCall{}
	rows, err := s.query.QueryContext(ctx, `SELECT id,name FROM tool_calls WHERE session_id=? ORDER BY id`, id)
	if err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("query tool calls: %w", err)
	}
	for rows.Next() {
		var callID, name string
		if err = rows.Scan(&callID, &name); err != nil {
			_ = rows.Close()
			return ToolOutcomeReport{}, fmt.Errorf("scan tool call: %w", err)
		}
		calls[callID] = &outcomeCall{name: name}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return ToolOutcomeReport{}, fmt.Errorf("read tool calls: %w", err)
	}
	if err = rows.Close(); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("close tool calls: %w", err)
	}
	report.AnalyzedCalls = len(calls)
	rows, err = s.query.QueryContext(ctx, `SELECT call_id,name,content,is_error FROM tool_results WHERE session_id=? ORDER BY id`, id)
	if err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("query tool results: %w", err)
	}
	for rows.Next() {
		var callID, name, content string
		var flag sql.NullBool
		if err = rows.Scan(&callID, &name, &content, &flag); err != nil {
			_ = rows.Close()
			return ToolOutcomeReport{}, fmt.Errorf("scan tool result: %w", err)
		}
		call, exists := calls[callID]
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
		return ToolOutcomeReport{}, fmt.Errorf("read tool results: %w", err)
	}
	if err = rows.Close(); err != nil {
		return ToolOutcomeReport{}, fmt.Errorf("close tool results: %w", err)
	}
	report.AnalyzedResults = report.TotalResults
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
	return report, nil
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
