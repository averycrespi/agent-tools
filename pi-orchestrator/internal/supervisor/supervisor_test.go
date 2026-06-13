package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
)

func TestExecuteRunsStepsSeriallyInWorkflowOrderWithSharedWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, run := supervisorTestRun(t)
	defer db.Close() //nolint:errcheck
	runner := &recordingRunner{results: []StepResult{{PDTaskID: "pd-1", PDRunID: "pd-1-run-1", State: store.StateSucceeded}, {PDTaskID: "pd-2", PDRunID: "pd-2-run-1", State: store.StateSucceeded}}}
	def := &workflow.Definition{
		Name:   "sample",
		Agents: map[string]workflow.Agent{"reviewer": {Model: "gpt-5.1-codex"}},
		Steps: []workflow.Step{
			{ID: "first", Agent: "reviewer", Prompt: "first"},
			{ID: "second", Agent: "reviewer", Needs: []string{"first"}, Prompt: "second"},
		},
	}

	if err := Execute(ctx, db, runner, def, run); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %+v, want 2", runner.calls)
	}
	if runner.calls[0].StepID != "first" || runner.calls[1].StepID != "second" {
		t.Fatalf("calls = %+v, want workflow order", runner.calls)
	}
	if runner.calls[0].WorktreePath != run.WorktreePath || runner.calls[1].WorktreePath != run.WorktreePath {
		t.Fatalf("calls = %+v, want shared workflow worktree", runner.calls)
	}
	detail, err := db.GetWorkflowRunDetail(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if detail.Run.State != store.StateSucceeded {
		t.Fatalf("workflow state = %s, want succeeded", detail.Run.State)
	}
	if detail.Steps[0].PDTaskID != "pd-1" || detail.Steps[1].PDRunID != "pd-2-run-1" {
		t.Fatalf("steps = %+v, want backing pd metadata", detail.Steps)
	}
}

func TestExecuteFailsStepWhenRequiredArtifactMissingAndSkipsDependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, run := supervisorTestRun(t)
	defer db.Close() //nolint:errcheck
	runner := &recordingRunner{results: []StepResult{{PDTaskID: "pd-1", PDRunID: "pd-1-run-1", State: store.StateSucceeded}}}
	def := &workflow.Definition{
		Name:   "sample",
		Agents: map[string]workflow.Agent{"reviewer": {Model: "gpt-5.1-codex"}},
		Steps: []workflow.Step{
			{ID: "first", Agent: "reviewer", Prompt: "first", Artifacts: []workflow.Artifact{{Name: "out", Path: "out.md", Required: true}}},
			{ID: "second", Agent: "reviewer", Needs: []string{"first"}, Prompt: "second"},
		},
	}

	if err := Execute(ctx, db, runner, def, run); err == nil {
		t.Fatal("Execute() error = nil, want missing artifact error")
	}
	detail, err := db.GetWorkflowRunDetail(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if detail.Run.State != store.StateFailed {
		t.Fatalf("workflow state = %s, want failed", detail.Run.State)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].State != store.StateFailed || detail.Steps[1].State != store.StateSkipped {
		t.Fatalf("steps = %+v, want failed then skipped", detail.Steps)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Exists {
		t.Fatalf("artifacts = %+v, want missing recorded artifact", detail.Artifacts)
	}
}

type recordingRunner struct {
	results []StepResult
	calls   []StepRequest
}

func (r *recordingRunner) RunStep(_ context.Context, req StepRequest) (StepResult, error) {
	r.calls = append(r.calls, req)
	if len(r.results) == 0 {
		return StepResult{State: store.StateSucceeded}, nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func supervisorTestRun(t *testing.T) (*store.Store, store.WorkflowRun) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: req.ID, Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: filepath.Join(t.TempDir(), "artifacts", "run-1"), State: store.StateRunning, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("CreateRunRequestWithWorkflowRun() error = %v", err)
	}
	return db, run
}
