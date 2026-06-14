package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestDashboardMuxExposesEventsRoute(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close() //nolint:errcheck
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	reqRecord := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts", State: store.StateSucceeded, SupervisorLogPath: "/logs/supervisor.log", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), reqRecord, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

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
