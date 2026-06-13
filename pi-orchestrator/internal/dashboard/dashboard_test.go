package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestAPIsRequireBearerToken(t *testing.T) {
	db := dashboardTestStore(t)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
}

func TestRunsAPIReturnsReadOnlySummaries(t *testing.T) {
	db := dashboardTestStore(t)
	seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var summaries []store.WorkflowRunSummary
	if err := json.Unmarshal(resp.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Run.ID != "run-1" || summaries[0].StepCounts[store.StateSucceeded] != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
}

func TestRunDetailAPIRejectsMutationMethods(t *testing.T) {
	db := dashboardTestStore(t)
	seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/runs/run-1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
}

func dashboardTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedDashboardRun(t *testing.T, db *store.Store) {
	t.Helper()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts", State: store.StateSucceeded, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: store.StateSucceeded, StartedAt: now, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step, nil); err != nil {
		t.Fatalf("create step: %v", err)
	}
}
