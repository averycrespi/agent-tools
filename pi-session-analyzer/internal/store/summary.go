package store

import (
	"context"
	"database/sql"
	"errors"
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
	summary := Summary{Session: SessionRow{ID: id}, Tools: map[string]ToolStats{}, GoalState: "no goal started"}
	if err = s.db.QueryRowContext(ctx, `SELECT timestamp,cwd,source_path,schema_drift,total_records FROM sessions WHERE id=?`, id).Scan(&summary.Session.Timestamp, &summary.Session.CWD, &summary.Session.SourcePath, &summary.Session.SchemaDrift, &summary.Session.TotalRecords); err != nil {
		return Summary{}, err
	}
	summary.SchemaDrift = summary.Session.SchemaDrift
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_write_tokens),0),COALESCE((SELECT stop_reason FROM messages WHERE session_id=? AND role='assistant' AND stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1),'') FROM messages WHERE session_id=?`, id, id).Scan(&summary.Cost, &summary.OutputTokens, &summary.ReasoningTokens, &summary.CacheReadTokens, &summary.CacheWriteTokens, &summary.FinalStopReason); err != nil {
		return Summary{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,COUNT(*) FROM tool_calls WHERE session_id=? GROUP BY name`, id)
	if err != nil {
		return Summary{}, err
	}
	for rows.Next() {
		var name string
		var calls int
		if err = rows.Scan(&name, &calls); err != nil {
			_ = rows.Close()
			return Summary{}, err
		}
		summary.Tools[name] = ToolStats{Calls: calls}
	}
	if err = rows.Close(); err != nil {
		return Summary{}, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT COALESCE(NULLIF(r.name,''),c.name,''),COUNT(*) FROM tool_results r LEFT JOIN tool_calls c ON c.session_id=r.session_id AND c.id=r.call_id WHERE r.session_id=? AND r.is_error=1 GROUP BY COALESCE(NULLIF(r.name,''),c.name,'')`, id)
	if err != nil {
		return Summary{}, err
	}
	for rows.Next() {
		var name string
		var errors int
		if err = rows.Scan(&name, &errors); err != nil {
			_ = rows.Close()
			return Summary{}, err
		}
		stats := summary.Tools[name]
		stats.Errors = errors
		summary.Tools[name] = stats
	}
	if err = rows.Close(); err != nil {
		return Summary{}, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE session_id=? AND type='compaction'`, id).Scan(&summary.Compactions); err != nil {
		return Summary{}, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custom_messages WHERE session_id=? AND type='broker-guard'`, id).Scan(&summary.BrokerGuards); err != nil {
		return Summary{}, err
	}
	var goalStatus string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM custom_state WHERE session_id=? AND type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1`, id).Scan(&goalStatus)
	switch {
	case err == nil && goalStatus == "":
		summary.GoalState = "started (status unavailable)"
	case err == nil:
		summary.GoalState = goalStatus
	case !errors.Is(err, sql.ErrNoRows):
		return Summary{}, err
	}
	summary.Findings, err = s.Findings(ctx, id)
	if err != nil {
		return Summary{}, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT detector,status,error_summary,generation FROM detector_runs WHERE session_id=? ORDER BY detector`, id)
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
