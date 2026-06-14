package store

import (
	"context"
	"testing"
	"time"
)

func TestListWorkflowRunSummariesIncludesStepCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)
	steps := []StepRun{
		{WorkflowRunID: "run-1", StepID: "first", Agent: "reviewer", ExecutionIndex: 0, State: StateSucceeded, StartedAt: now, UpdatedAt: now},
		{WorkflowRunID: "run-1", StepID: "second", Agent: "reviewer", ExecutionIndex: 1, State: StateFailed, StartedAt: now, UpdatedAt: now},
		{WorkflowRunID: "run-1", StepID: "third", Agent: "reviewer", ExecutionIndex: 2, State: StateSkipped, StartedAt: now, UpdatedAt: now},
	}
	for _, step := range steps {
		if err := db.CreateStepRun(ctx, step, nil); err != nil {
			t.Fatalf("CreateStepRun(%s) error = %v", step.StepID, err)
		}
	}

	summaries, err := db.ListWorkflowRunSummaries(ctx)
	if err != nil {
		t.Fatalf("ListWorkflowRunSummaries() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.ID != "run-1" || summary.Workflow != "sample" || summary.StepCounts[StateSucceeded] != 1 || summary.StepCounts[StateFailed] != 1 || summary.StepCounts[StateSkipped] != 1 {
		t.Fatalf("summary = %+v, want run and step counts", summary)
	}
}
