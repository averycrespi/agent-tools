package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

func TestAPIsRequireBearerToken(t *testing.T) {
	db := dashboardTestStore(t)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/runs", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/runs", nil)
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

func TestDashboardTokenURLSetsCookieThenRedirects(t *testing.T) {
	db := dashboardTestStore(t)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/?token=secret", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if location := resp.Header().Get("Location"); location != "/dashboard/" {
		t.Fatalf("Location = %q, want /dashboard/", location)
	}
	cookies := resp.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "po-auth" || cookies[0].Value != "secret" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %+v, want HttpOnly po-auth cookie", cookies)
	}
}

func TestAPIsAcceptDashboardCookie(t *testing.T) {
	db := dashboardTestStore(t)
	seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")
	server := httptest.NewServer(handler)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := server.Client()
	client.Jar = jar
	resp, err := client.Get(server.URL + "/dashboard/?token=secret")
	if err != nil {
		t.Fatalf("GET token URL: %v", err)
	}
	_ = resp.Body.Close()

	resp, err = client.Get(server.URL + "/dashboard/api/runs")
	if err != nil {
		t.Fatalf("GET runs: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestEventsReturnsReadOnlySSESnapshot(t *testing.T) {
	db := dashboardTestStore(t)
	seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/events", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event: snapshot") || !strings.Contains(body, "run-1") {
		t.Fatalf("body = %q, want snapshot event with run id", body)
	}
}

func TestDashboardUIExposesRunDetailAndLogsExplorer(t *testing.T) {
	db := dashboardTestStore(t)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, want := range []string{"/dashboard/api/runs/", "/logs", "Run detail", "Supervisor logs"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %q, want substring %q", body, want)
		}
	}
}

func TestRunDetailAPIRejectsMutationMethods(t *testing.T) {
	db := dashboardTestStore(t)
	seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/api/runs/run-1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
}

func TestRunLogsAPIReturnsBoundedSupervisorLogWindow(t *testing.T) {
	db := dashboardTestStore(t)
	logPath := seedDashboardRun(t, db)
	handler := NewHandler(db, "secret")
	if err := os.WriteFile(logPath, []byte("hello supervisor\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api/runs/run-1/logs", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if got["path"] != logPath || got["content"] != "hello supervisor\n" || got["truncated"] != false {
		t.Fatalf("logs = %+v, want log window", got)
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

func seedDashboardRun(t *testing.T, db *store.Store) string {
	t.Helper()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	logPath := filepath.Join(t.TempDir(), "supervisor.log")
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: "req-1", Workflow: "sample", DefinitionHash: "hash", InputsJSON: `{}`, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts", State: store.StateSucceeded, SupervisorLogPath: logPath, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	step := store.StepRun{WorkflowRunID: "run-1", StepID: "review", Agent: "reviewer", ExecutionIndex: 0, State: store.StateSucceeded, StartedAt: now, UpdatedAt: now}
	if err := db.CreateStepRun(context.Background(), step, nil); err != nil {
		t.Fatalf("create step: %v", err)
	}
	return logPath
}
