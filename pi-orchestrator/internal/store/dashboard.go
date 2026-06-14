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
	countsByRun, err := s.stepCountsByWorkflowRun(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]WorkflowRunSummary, 0, len(runs))
	for _, run := range runs {
		summaries = append(summaries, WorkflowRunSummary{Run: run, StepCounts: countsByRun[run.ID]})
	}
	return summaries, nil
}

func (s *Store) stepCountsByWorkflowRun(ctx context.Context) (map[string]map[State]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_run_id, state, COUNT(*) FROM step_runs GROUP BY workflow_run_id, state`)
	if err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	countsByRun := map[string]map[State]int{}
	for rows.Next() {
		var runID string
		var state string
		var count int
		if err := rows.Scan(&runID, &state, &count); err != nil {
			return nil, fmt.Errorf("scan step count: %w", err)
		}
		if countsByRun[runID] == nil {
			countsByRun[runID] = map[State]int{}
		}
		countsByRun[runID][State(state)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	return countsByRun, nil
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
