package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestDashboardMuxRunDetailIncludesStepsArtifactsAndPDMetadata(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close() //nolint:errcheck
	seedDashboardRun(t, db)

	handler := dashboardMux(db, "secret")
	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/runs/run-1", nil)
	req.AddCookie(&http.Cookie{Name: "po-auth", Value: "secret"})
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var detail store.WorkflowRunDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Run.ID != "run-1" || len(detail.Steps) != 1 || len(detail.Artifacts) != 1 {
		t.Fatalf("detail = %+v, want run, step, and artifact", detail)
	}
	if detail.Steps[0].PDTaskID != "pd-task-1" || detail.Steps[0].PDRunID != "pd-run-1" || detail.Steps[0].PDStdoutPath == "" {
		t.Fatalf("step = %+v, want backing pd metadata", detail.Steps[0])
	}
}

func TestDashboardMuxExposesEventsRoute(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close() //nolint:errcheck
	seedDashboardRun(t, db)

	handler := dashboardMux(db, "secret")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/events", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: "po-auth", Value: "secret"})
	resp := httptest.NewRecorder()
	timer := time.AfterFunc(5*time.Millisecond, cancel)
	defer timer.Stop()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "event: snapshot") {
		t.Fatalf("body = %q, want SSE snapshot", resp.Body.String())
	}
}

func seedDashboardRun(t *testing.T, db *store.Store) {
	t.Helper()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	reqRecord := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts", State: store.StateSucceeded, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), reqRecord, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: store.StateSucceeded, PDTaskID: "pd-task-1", PDRunID: "pd-run-1", PDStdoutPath: "/logs/stdout.log", StartedAt: now, UpdatedAt: now}
	artifact := store.Artifact{WorkflowRunID: "run-1", Name: "findings", RelativePath: "findings.md", AbsolutePath: "/artifacts/run-1/findings.md", Exists: true, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step); err != nil {
		t.Fatalf("create step: %v", err)
	}
	if err := db.UpsertArtifacts(context.Background(), []store.Artifact{artifact}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
}
