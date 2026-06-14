package store

import (
	"context"
	"fmt"
	"time"
)

type WorkflowRunSummary struct {
	ID           string        `json:"id"`
	Workflow     string        `json:"workflow"`
	State        State         `json:"state"`
	Repo         string        `json:"repo"`
	Branch       string        `json:"branch"`
	WorktreePath string        `json:"worktree_path"`
	ArtifactRoot string        `json:"artifact_root"`
	Outcome      string        `json:"outcome"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	StepCounts   map[State]int `json:"step_counts"`
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
		stepCounts := countsByRun[run.ID]
		if stepCounts == nil {
			stepCounts = map[State]int{}
		}
		summaries = append(summaries, WorkflowRunSummary{ID: run.ID, Workflow: run.Workflow, State: run.State, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath, ArtifactRoot: run.ArtifactRoot, Outcome: run.Outcome, CreatedAt: run.CreatedAt.Format(time.RFC3339), UpdatedAt: run.UpdatedAt.Format(time.RFC3339), StepCounts: stepCounts})
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
