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

func TestExecuteRunsReadyStepsInFileOrderWhenDependenciesAppearLater(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, run := supervisorTestRun(t)
	defer db.Close() //nolint:errcheck
	runner := &recordingRunner{results: []StepResult{{PDTaskID: "pd-build", PDRunID: "pd-build-run-1", State: store.StateSucceeded}, {PDTaskID: "pd-deploy", PDRunID: "pd-deploy-run-1", State: store.StateSucceeded}}}
	def := &workflow.Definition{
		Name:   "sample",
		Agents: map[string]workflow.Agent{"runner": {Model: "gpt-5.1-codex"}},
		Steps: []workflow.Step{
			{ID: "deploy", Agent: "runner", Needs: []string{"build"}, Prompt: "deploy"},
			{ID: "build", Agent: "runner", Prompt: "build"},
		},
	}

	if err := Execute(ctx, db, runner, def, run); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(runner.calls) != 2 || runner.calls[0].StepID != "build" || runner.calls[1].StepID != "deploy" {
		t.Fatalf("calls = %+v, want build then deploy", runner.calls)
	}
	detail, err := db.GetWorkflowRunDetail(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if detail.Steps[0].StepID != "build" || detail.Steps[1].StepID != "deploy" {
		t.Fatalf("steps = %+v, want execution order build then deploy", detail.Steps)
	}
}

func TestExecuteRendersPromptInputsAndArtifactPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, run := supervisorTestRun(t)
	defer db.Close() //nolint:errcheck
	runner := &recordingRunner{results: []StepResult{{PDTaskID: "pd-1", PDRunID: "pd-1-run-1", State: store.StateSucceeded}}}
	def := &workflow.Definition{
		Name:   "sample",
		Inputs: map[string]workflow.InputSchema{"pr_number": {Type: workflow.InputInteger}},
		Agents: map[string]workflow.Agent{"reviewer": {Model: "gpt-5.1-codex"}},
		Steps:  []workflow.Step{{ID: "review", Agent: "reviewer", Prompt: `Review PR #{{ .Inputs.pr_number }} and write {{ artifact_path "out" }}`, Artifacts: []workflow.Artifact{{Name: "out", Path: "out.md"}}}},
	}
	run.InputsJSON = `{"pr_number":42}`

	if err := Execute(ctx, db, runner, def, run); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := filepath.Join(run.ArtifactRoot, "out.md")
	if len(runner.calls) != 1 || runner.calls[0].Prompt != "Review PR #42 and write "+want {
		t.Fatalf("prompt = %q, want rendered artifact path %q", runner.calls[0].Prompt, want)
	}
}

func TestExecuteRendersPreviousStepArtifactPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, run := supervisorTestRun(t)
	defer db.Close() //nolint:errcheck
	runner := &recordingRunner{results: []StepResult{{PDTaskID: "pd-1", PDRunID: "pd-1-run-1", State: store.StateSucceeded}, {PDTaskID: "pd-2", PDRunID: "pd-2-run-1", State: store.StateSucceeded}}}
	def := &workflow.Definition{
		Name:   "sample",
		Agents: map[string]workflow.Agent{"reviewer": {Model: "gpt-5.1-codex"}},
		Steps: []workflow.Step{
			{ID: "first", Agent: "reviewer", Prompt: `write {{ artifact_path "findings" }}`, Artifacts: []workflow.Artifact{{Name: "findings", Path: "findings.md"}}},
			{ID: "second", Agent: "reviewer", Needs: []string{"first"}, Prompt: `read {{ artifact_path "findings" }} and write {{ artifact_path "final" }}`, Artifacts: []workflow.Artifact{{Name: "final", Path: "final.md"}}},
		},
	}

	if err := Execute(ctx, db, runner, def, run); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	findingsPath := filepath.Join(run.ArtifactRoot, "findings.md")
	finalPath := filepath.Join(run.ArtifactRoot, "final.md")
	if len(runner.calls) != 2 || runner.calls[1].Prompt != "read "+findingsPath+" and write "+finalPath {
		t.Fatalf("second prompt = %q, want previous artifact path %q and final path %q", runner.calls[1].Prompt, findingsPath, finalPath)
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
