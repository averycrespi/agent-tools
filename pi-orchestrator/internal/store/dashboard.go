package store

import (
	"context"
	"fmt"
)

type WorkflowRunSummary struct {
	Run        WorkflowRun
	StepCounts map[State]int
}

func (s *Store) ListWorkflowRunSummaries(ctx context.Context) ([]WorkflowRunSummary, error) {
	runs, err := s.ListWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]WorkflowRunSummary, 0, len(runs))
	for _, run := range runs {
		counts, err := s.stepCounts(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, WorkflowRunSummary{Run: run, StepCounts: counts})
	}
	return summaries, nil
}

func (s *Store) stepCounts(ctx context.Context, workflowRunID string) (map[State]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM step_runs WHERE workflow_run_id = ? GROUP BY state`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	counts := map[State]int{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("scan step count: %w", err)
		}
		counts[State(state)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	return counts, nil
}
