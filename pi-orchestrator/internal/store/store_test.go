package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateRunRequestWithWorkflowRunPersistsV1Metadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	req := RunRequest{
		ID:         "req-1",
		Workflow:   "pr-review",
		InputsJSON: `{"repo":"/repo","pr_number":42}`,
		Source:     "cli",
		CreatedAt:  now,
	}
	run := WorkflowRun{
		ID:                "po-20260613-120000-abcd",
		RequestID:         req.ID,
		Workflow:          "pr-review",
		DefinitionHash:    "sha256:abc",
		DefinitionYAML:    "name: pr-review\n",
		InputsJSON:        req.InputsJSON,
		Repo:              "/repo",
		Branch:            "po/pr-review-abcd",
		WorktreePath:      "/worktrees/pr-review-abcd",
		ArtifactRoot:      "/artifacts/po-20260613-120000-abcd",
		State:             StateStarting,
		SupervisorLogPath: "/logs/supervisor.log",
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := db.CreateRunRequestWithWorkflowRun(ctx, req, run); err != nil {
		t.Fatalf("CreateRunRequestWithWorkflowRun() error = %v", err)
	}

	got, err := db.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun() error = %v", err)
	}
	if got.RequestID != req.ID || got.Workflow != "pr-review" || got.DefinitionYAML != "name: pr-review\n" || got.State != StateStarting {
		t.Fatalf("run = %+v, want persisted request/workflow/state", got)
	}
	if got.ArtifactRoot != run.ArtifactRoot || got.SupervisorLogPath != run.SupervisorLogPath {
		t.Fatalf("run paths = %+v, want artifact and supervisor log paths", got)
	}
}

func TestCreateStepRunAndArtifactsPersistsBackingPDMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)

	step := StepRun{
		WorkflowRunID:  "run-1",
		StepID:         "review",
		Agent:          "reviewer",
		ExecutionIndex: 0,
		State:          StateRunning,
		PDTaskID:       "pd-task-1",
		PDRunID:        "pd-task-1-run-1",
		StartedAt:      now,
		UpdatedAt:      now,
	}
	artifacts := []Artifact{{
		WorkflowRunID: "run-1",
		StepID:        "review",
		Name:          "findings",
		RelativePath:  "findings.md",
		AbsolutePath:  "/artifacts/run-1/findings.md",
		Required:      true,
		Exists:        false,
		UpdatedAt:     now,
	}}

	if err := db.CreateStepRun(ctx, step, artifacts); err != nil {
		t.Fatalf("CreateStepRun() error = %v", err)
	}

	detail, err := db.GetWorkflowRunDetail(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].PDTaskID != "pd-task-1" || detail.Steps[0].PDRunID != "pd-task-1-run-1" {
		t.Fatalf("steps = %+v, want backing pd metadata", detail.Steps)
	}
	if len(detail.Artifacts) != 1 || !detail.Artifacts[0].Required || detail.Artifacts[0].Exists {
		t.Fatalf("artifacts = %+v, want required missing artifact", detail.Artifacts)
	}
}

func TestUpdateArtifactExistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)
	artifact := Artifact{WorkflowRunID: "run-1", StepID: "review", Name: "findings", RelativePath: "findings.md", AbsolutePath: "/artifacts/run-1/findings.md", Required: true, Exists: false, UpdatedAt: now}
	if err := db.CreateStepRun(ctx, StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: StateRunning, StartedAt: now, UpdatedAt: now}, []Artifact{artifact}); err != nil {
		t.Fatalf("CreateStepRun() error = %v", err)
	}
	artifact.Exists = true
	artifact.UpdatedAt = now.Add(time.Minute)

	if err := db.UpdateArtifactExistence(ctx, []Artifact{artifact}); err != nil {
		t.Fatalf("UpdateArtifactExistence() error = %v", err)
	}
	detail, err := db.GetWorkflowRunDetail(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if len(detail.Artifacts) != 1 || !detail.Artifacts[0].Exists {
		t.Fatalf("artifacts = %+v, want existing artifact", detail.Artifacts)
	}
}

func TestRecordWorkflowCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)

	if err := db.RecordWorkflowCleanup(ctx, "run-1", "removed", "", now.Add(time.Minute)); err != nil {
		t.Fatalf("RecordWorkflowCleanup() error = %v", err)
	}
	run, err := db.GetWorkflowRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRun() error = %v", err)
	}
	if run.CleanupStatus != "removed" || run.CleanupError != "" || !run.CleanupAttemptedAt.Valid {
		t.Fatalf("run = %+v, want cleanup metadata", run)
	}
}

func TestDeleteWorkflowRunRemovesRunRequestMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)

	if err := db.DeleteWorkflowRun(ctx, "run-1"); err != nil {
		t.Fatalf("DeleteWorkflowRun() error = %v", err)
	}
	if _, err := db.GetRunRequest(ctx, "req-1"); err == nil {
		t.Fatal("GetRunRequest() error = nil, want removed run request")
	}
}

func TestUpdateWorkflowAndStepState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	createWorkflowRun(t, ctx, db, now)
	if err := db.CreateStepRun(ctx, StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: StateRunning, StartedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatalf("CreateStepRun() error = %v", err)
	}

	if err := db.UpdateStepState(ctx, "run-1", "review", StateFailed, "missing required artifact", now.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateStepState() error = %v", err)
	}
	if err := db.UpdateWorkflowRunState(ctx, "run-1", StateFailed, "step review failed", now.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateWorkflowRunState() error = %v", err)
	}

	detail, err := db.GetWorkflowRunDetail(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRunDetail() error = %v", err)
	}
	if detail.Run.State != StateFailed || detail.Run.Outcome != "step review failed" {
		t.Fatalf("run = %+v, want failed outcome", detail.Run)
	}
	if detail.Steps[0].State != StateFailed || detail.Steps[0].Outcome != "missing required artifact" {
		t.Fatalf("step = %+v, want failed outcome", detail.Steps[0])
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return db
}

func createWorkflowRun(t *testing.T, ctx context.Context, db *Store, now time.Time) {
	t.Helper()
	req := RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := WorkflowRun{ID: "run-1", RequestID: req.ID, Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "branch", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/run-1", State: StateRunning, SupervisorLogPath: "/logs/run-1.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(ctx, req, run); err != nil {
		t.Fatalf("CreateRunRequestWithWorkflowRun() error = %v", err)
	}
}
