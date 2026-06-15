package store

import (
	"context"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

type WorkflowRunSummary struct {
	ID           string        `json:"id"`
	Workflow     string        `json:"workflow"`
	State        State         `json:"state"`
	Repo         string        `json:"repo"`
	Branch       string        `json:"branch"`
	WorktreePath string        `json:"worktree_path"`
	ArtifactRoot string        `json:"artifact_root"`
	InputsJSON   string        `json:"inputs_json"`
	Outcome      string        `json:"outcome"`
	CreatedAt    string        `json:"created_at"`
	UpdatedAt    string        `json:"updated_at"`
	StepCounts   map[State]int `json:"step_counts"`
	StepTotal    int           `json:"step_total"`
	StepPending  int           `json:"step_pending"`
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
	return workflowRunSummaries(runs, countsByRun), nil
}

func (s *Store) ListWorkflowRunSummariesPage(ctx context.Context, limit int, offset int) ([]WorkflowRunSummary, error) {
	runs, err := s.listWorkflowRunsPage(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	countsByRun, err := s.stepCountsForWorkflowRunPage(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	return workflowRunSummaries(runs, countsByRun), nil
}

func workflowRunSummaries(runs []WorkflowRun, countsByRun map[string]map[State]int) []WorkflowRunSummary {
	summaries := make([]WorkflowRunSummary, 0, len(runs))
	for _, run := range runs {
		stepCounts := countsByRun[run.ID]
		if stepCounts == nil {
			stepCounts = map[State]int{}
		}
		stepTotal, stepPending := stepProgress(run.DefinitionYAML, stepCounts)
		summaries = append(summaries, WorkflowRunSummary{ID: run.ID, Workflow: run.Workflow, State: run.State, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath, ArtifactRoot: run.ArtifactRoot, InputsJSON: run.InputsJSON, Outcome: run.Outcome, CreatedAt: run.CreatedAt.Format(time.RFC3339), UpdatedAt: run.UpdatedAt.Format(time.RFC3339), StepCounts: stepCounts, StepTotal: stepTotal, StepPending: stepPending})
	}
	return summaries
}

type workflowStepList struct {
	Steps []struct{} `yaml:"steps"`
}

func stepProgress(definitionYAML string, counts map[State]int) (int, int) {
	recorded := 0
	for _, count := range counts {
		recorded += count
	}
	total := recorded
	var definition workflowStepList
	if definitionYAML != "" && yaml.Unmarshal([]byte(definitionYAML), &definition) == nil && len(definition.Steps) > 0 {
		total = len(definition.Steps)
	}
	pending := total - recorded
	if pending < 0 {
		pending = 0
	}
	return total, pending
}

func (s *Store) stepCountsByWorkflowRun(ctx context.Context) (map[string]map[State]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_run_id, state, COUNT(*) FROM step_runs GROUP BY workflow_run_id, state`)
	if err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	return scanStepCounts(rows)
}

func (s *Store) stepCountsForWorkflowRunPage(ctx context.Context, limit int, offset int) (map[string]map[State]int, error) {
	rows, err := s.db.QueryContext(ctx, `WITH selected_runs AS (SELECT id FROM workflow_runs ORDER BY created_at DESC LIMIT ? OFFSET ?) SELECT step_runs.workflow_run_id, step_runs.state, COUNT(*) FROM step_runs JOIN selected_runs ON selected_runs.id = step_runs.workflow_run_id GROUP BY step_runs.workflow_run_id, step_runs.state`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query step counts: %w", err)
	}
	return scanStepCounts(rows)
}

func scanStepCounts(rows interface {
	Close() error
	Next() bool
	Scan(...any) error
	Err() error
}) (map[string]map[State]int, error) {
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
