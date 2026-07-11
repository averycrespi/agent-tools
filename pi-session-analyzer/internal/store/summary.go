package store

import (
	"context"
	"fmt"
)

type ToolStats struct {
	Calls  int `json:"calls"`
	Errors int `json:"errors"`
}

type DetectorRun struct {
	Detector, Status, Error string
	Generation              int
}

type Summary struct {
	Session                                                          SessionRow
	Cost                                                             float64
	OutputTokens, ReasoningTokens, CacheReadTokens, CacheWriteTokens int64
	Tools                                                            map[string]ToolStats
	FinalStopReason, GoalState                                       string
	Compactions, BrokerGuards, SchemaDrift                           int
	Findings                                                         []FindingRow
	DetectorRuns                                                     []DetectorRun
}

func (s *Store) SessionSummary(ctx context.Context, prefix string) (Summary, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return Summary{}, err
	}
	session, err := s.LoadSession(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	var source string
	if err = s.db.QueryRowContext(ctx, `SELECT source_path FROM sessions WHERE id=?`, id).Scan(&source); err != nil {
		return Summary{}, err
	}
	summary := Summary{Session: SessionRow{ID: id, Timestamp: session.Timestamp, CWD: session.CWD, SourcePath: source, SchemaDrift: session.Stats.SchemaDrift, TotalRecords: session.Stats.Total}, Tools: map[string]ToolStats{}, SchemaDrift: session.Stats.SchemaDrift, GoalState: "no goal started"}
	for _, m := range session.Messages {
		summary.Cost += m.Cost
		summary.OutputTokens += m.OutputTokens
		summary.ReasoningTokens += m.ReasoningTokens
		summary.CacheReadTokens += m.CacheReadTokens
		summary.CacheWriteTokens += m.CacheWriteTokens
		if m.Role == "assistant" && m.StopReason != "" {
			summary.FinalStopReason = m.StopReason
		}
	}
	for _, c := range session.ToolCalls {
		v := summary.Tools[c.Name]
		v.Calls++
		summary.Tools[c.Name] = v
	}
	for _, r := range session.ToolResults {
		if r.IsError != nil && *r.IsError {
			v := summary.Tools[r.Name]
			v.Errors++
			summary.Tools[r.Name] = v
		}
	}
	for _, e := range session.Events {
		if e.Type == "compaction" {
			summary.Compactions++
		}
	}
	for _, m := range session.CustomMessages {
		if m.Type == "broker-guard" {
			summary.BrokerGuards++
		}
	}
	for _, state := range session.CustomStates {
		if state.Type == "goal-state" {
			if state.Status == "" {
				summary.GoalState = "started (status unavailable)"
			} else {
				summary.GoalState = state.Status
			}
		}
	}
	summary.Findings, err = s.Findings(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT detector,status,error_summary,generation FROM detector_runs WHERE session_id=? ORDER BY detector`, id)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var run DetectorRun
		if err := rows.Scan(&run.Detector, &run.Status, &run.Error, &run.Generation); err != nil {
			return Summary{}, fmt.Errorf("scan detector run: %w", err)
		}
		summary.DetectorRuns = append(summary.DetectorRuns, run)
	}
	return summary, rows.Err()
}
