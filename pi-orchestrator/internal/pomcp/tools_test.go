package pomcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func TestToolDefinitionsAreReadOnlyLocal(t *testing.T) {
	h := NewHandler(nil, t.TempDir())
	tools := h.Tools()

	want := []string{"list_workflows", "get_workflow", "list_workflow_runs", "get_workflow_run", "get_workflow_run_logs", "get_step_logs"}
	if got := toolNames(tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	for _, tool := range tools {
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s missing read-only hint", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %s missing local closed-world hint", tool.Name)
		}
	}
}

func TestListWorkflowsSummarizesDefinitionsAndInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "sample.yaml", "prompt secret from list")
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: broken\nrepo: /repo\nagents: {}\nsteps: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, dir)
	req := toolRequest("list_workflows", nil)

	result, err := h.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result error = %s", toolText(t, result))
	}
	body := toolText(t, result)
	if strings.Contains(body, "prompt secret from list") {
		t.Fatalf("list_workflows exposed prompt body: %s", body)
	}
	var payload struct {
		Workflows []WorkflowSummary `json:"workflows"`
	}
	decodeToolText(t, result, &payload)
	if len(payload.Workflows) != 2 {
		t.Fatalf("workflow count = %d, want 2: %+v", len(payload.Workflows), payload.Workflows)
	}
	if payload.Workflows[0].Name != "broken" || payload.Workflows[0].Valid {
		t.Fatalf("first workflow = %+v, want invalid broken entry", payload.Workflows[0])
	}
	if payload.Workflows[1].Name != "sample" || !payload.Workflows[1].Valid {
		t.Fatalf("second workflow = %+v, want valid sample entry", payload.Workflows[1])
	}
	if payload.Workflows[1].Inputs[0].Name != "ticket" || payload.Workflows[1].Agents[0].Model != "gpt-5" || payload.Workflows[1].Steps[0].ID != "plan" {
		t.Fatalf("summary = %+v, want parsed metadata", payload.Workflows[1])
	}
}

func TestGetWorkflowReturnsRawYAMLForNamedWorkflowAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "sample.yaml", "explicit prompt body")
	h := NewHandler(nil, dir)

	result, err := h.Handle(context.Background(), toolRequest("get_workflow", map[string]any{"workflow": "sample"}))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result error = %s", toolText(t, result))
	}
	body := toolText(t, result)
	if !strings.Contains(body, "explicit prompt body") {
		t.Fatalf("get_workflow body = %s, want explicit prompt", body)
	}
	var detail WorkflowDetail
	decodeToolText(t, result, &detail)
	if detail.RawYAML == "" || detail.Steps[0].Prompt != "explicit prompt body" {
		t.Fatalf("detail = %+v, want raw yaml and prompt", detail)
	}

	result, err = h.Handle(context.Background(), toolRequest("get_workflow", map[string]any{"workflow": "../sample"}))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !result.IsError || !strings.Contains(toolText(t, result), "path separators") {
		t.Fatalf("traversal result = %#v text %q, want tool error", result.IsError, toolText(t, result))
	}
}

func TestListWorkflowRunsAndGetWorkflowRunOmitSensitiveFields(t *testing.T) {
	st, runID := testStore(t)
	h := NewHandler(st, t.TempDir())

	listResult, err := h.Handle(context.Background(), toolRequest("list_workflow_runs", nil))
	if err != nil {
		t.Fatalf("list Handle() error = %v", err)
	}
	listBody := toolText(t, listResult)
	assertNotContainsSensitiveRunData(t, listBody)
	var listPayload struct {
		Runs []RunSummary `json:"runs"`
	}
	decodeToolText(t, listResult, &listPayload)
	if len(listPayload.Runs) != 1 || listPayload.Runs[0].StepTotal != 2 || listPayload.Runs[0].StepPending != 1 {
		t.Fatalf("runs = %+v, want one run with progress", listPayload.Runs)
	}

	detailResult, err := h.Handle(context.Background(), toolRequest("get_workflow_run", map[string]any{"run_id": runID}))
	if err != nil {
		t.Fatalf("detail Handle() error = %v", err)
	}
	detailBody := toolText(t, detailResult)
	assertNotContainsSensitiveRunData(t, detailBody)
	var detail RunDetail
	decodeToolText(t, detailResult, &detail)
	if detail.Run.ID != runID || detail.Run.SupervisorLogPath == "" || detail.Steps[0].PDTaskID != "pd-task-1" || detail.Artifacts[0].Name != "report" {
		t.Fatalf("detail = %+v, want run, step, and artifact metadata", detail)
	}
}

func TestLogToolsReturnBoundedWindows(t *testing.T) {
	st, runID := testStore(t)
	h := NewHandler(st, t.TempDir())

	result, err := h.Handle(context.Background(), toolRequest("get_workflow_run_logs", map[string]any{"run_id": runID, "offset": float64(2), "limit": float64(5)}))
	if err != nil {
		t.Fatalf("supervisor logs Handle() error = %v", err)
	}
	var supervisor LogWindow
	decodeToolText(t, result, &supervisor)
	if supervisor.Stream != "supervisor" || supervisor.Content != "pervi" || supervisor.NextOffset != 7 || supervisor.Size != 15 || !supervisor.Truncated {
		t.Fatalf("supervisor window = %+v", supervisor)
	}

	result, err = h.Handle(context.Background(), toolRequest("get_step_logs", map[string]any{"run_id": runID, "step_id": "plan", "stream": "stderr", "offset": float64(0), "limit": float64(6)}))
	if err != nil {
		t.Fatalf("step logs Handle() error = %v", err)
	}
	var step LogWindow
	decodeToolText(t, result, &step)
	if step.Stream != "stderr" || step.Content != "stderr" || step.Size != 14 || !step.Truncated {
		t.Fatalf("step window = %+v", step)
	}
}

func TestLogValidationAndMissingIDsReturnToolErrors(t *testing.T) {
	st, runID := testStore(t)
	h := NewHandler(st, t.TempDir())
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		message string
	}{
		{name: "missing run id", tool: "get_workflow_run_logs", args: map[string]any{}, message: "run_id is required"},
		{name: "negative offset", tool: "get_workflow_run_logs", args: map[string]any{"run_id": runID, "offset": float64(-1)}, message: "offset must be a non-negative integer"},
		{name: "fractional limit", tool: "get_workflow_run_logs", args: map[string]any{"run_id": runID, "limit": 1.5}, message: "limit must be a non-negative integer"},
		{name: "oversized limit", tool: "get_workflow_run_logs", args: map[string]any{"run_id": runID, "limit": float64(MaxLogLimit + 1)}, message: "limit must be less than or equal to 1048576"},
		{name: "missing step", tool: "get_step_logs", args: map[string]any{"run_id": runID}, message: "step_id is required"},
		{name: "bad stream", tool: "get_step_logs", args: map[string]any{"run_id": runID, "step_id": "plan", "stream": "events"}, message: "stream must be stdout or stderr"},
		{name: "non string stream", tool: "get_step_logs", args: map[string]any{"run_id": runID, "step_id": "plan", "stream": float64(123)}, message: "stream must be stdout or stderr"},
		{name: "missing run id for detail", tool: "get_workflow_run", args: map[string]any{}, message: "run_id is required"},
		{name: "missing run id for step logs", tool: "get_step_logs", args: map[string]any{"step_id": "plan"}, message: "run_id is required"},
		{name: "unknown step", tool: "get_step_logs", args: map[string]any{"run_id": runID, "step_id": "missing"}, message: "workflow step not found"},
		{name: "unknown run", tool: "get_workflow_run", args: map[string]any{"run_id": "missing"}, message: "workflow run not found"},
		{name: "unknown run for step logs", tool: "get_step_logs", args: map[string]any{"run_id": "missing", "step_id": "plan"}, message: "workflow run not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := h.Handle(context.Background(), toolRequest(tt.tool, tt.args))
			if err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if !result.IsError || !strings.Contains(toolText(t, result), tt.message) {
				t.Fatalf("result error = %v text = %q, want %q", result.IsError, toolText(t, result), tt.message)
			}
		})
	}
}

func TestLogReadFailuresReturnToolErrors(t *testing.T) {
	st, runID := testStore(t)
	run, err := st.GetWorkflowRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(run.SupervisorLogPath); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(st, t.TempDir())
	result, err := h.Handle(context.Background(), toolRequest("get_workflow_run_logs", map[string]any{"run_id": runID}))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !result.IsError || !strings.Contains(toolText(t, result), "no such file") {
		t.Fatalf("supervisor log result error = %v text = %q", result.IsError, toolText(t, result))
	}

	st, runID = testStore(t)
	step, err := st.GetWorkflowStepRun(context.Background(), runID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(step.PDStdoutPath); err != nil {
		t.Fatal(err)
	}
	h = NewHandler(st, t.TempDir())
	result, err = h.Handle(context.Background(), toolRequest("get_step_logs", map[string]any{"run_id": runID, "step_id": "plan"}))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !result.IsError || !strings.Contains(toolText(t, result), "no such file") {
		t.Fatalf("step log result error = %v text = %q", result.IsError, toolText(t, result))
	}
}

func TestStoreReadFailuresAndUnknownToolReturnToolErrors(t *testing.T) {
	boom := errors.New("store unavailable")
	h := NewHandler(failingStore{err: boom}, t.TempDir())
	for _, tool := range []string{"list_workflow_runs", "get_workflow_run", "get_workflow_run_logs", "get_step_logs"} {
		t.Run(tool, func(t *testing.T) {
			result, err := h.Handle(context.Background(), toolRequest(tool, map[string]any{"run_id": "run-1", "step_id": "plan"}))
			if err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if !result.IsError || !strings.Contains(toolText(t, result), boom.Error()) {
				t.Fatalf("result error = %v text = %q", result.IsError, toolText(t, result))
			}
		})
	}

	result, err := h.Handle(context.Background(), toolRequest("unknown", nil))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !result.IsError || !strings.Contains(toolText(t, result), "unknown tool") {
		t.Fatalf("unknown result error = %v text = %q", result.IsError, toolText(t, result))
	}
}

type failingStore struct {
	err error
}

func (s failingStore) ListWorkflowRunSummaries(context.Context) ([]store.WorkflowRunSummary, error) {
	return nil, s.err
}

func (s failingStore) GetWorkflowRunDetail(context.Context, string) (store.WorkflowRunDetail, error) {
	return store.WorkflowRunDetail{}, s.err
}

func (s failingStore) GetWorkflowRun(context.Context, string) (store.WorkflowRun, error) {
	return store.WorkflowRun{}, s.err
}

func (s failingStore) GetWorkflowStepRun(context.Context, string, string) (store.StepRun, error) {
	return store.StepRun{}, s.err
}

func toolRequest(name string, args map[string]any) gomcp.CallToolRequest {
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	if args != nil {
		req.Params.Arguments = args
	}
	return req
}

func toolNames(tools []gomcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func decodeToolText(t *testing.T, result *gomcp.CallToolResult, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(toolText(t, result)), out); err != nil {
		t.Fatalf("unmarshal tool text: %v\n%s", err, toolText(t, result))
	}
}

func toolText(t *testing.T, result *gomcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty tool result")
	}
	text, ok := result.Content[0].(gomcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want TextContent", result.Content[0])
	}
	return text.Text
}

func writeWorkflow(t *testing.T, dir, name, prompt string) {
	t.Helper()
	data := `name: sample
description: Sample workflow
repo: /repo
inputs:
  ticket:
    type: string
    required: true
agents:
  planner:
    model: gpt-5
    skills: [plan]
steps:
  - id: plan
    agent: planner
    prompt: ` + quoteYAML(prompt) + `
    artifacts:
      - name: plan
        path: plan.md
        required: true
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func quoteYAML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func testStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "po.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dir := t.TempDir()
	supervisorPath := filepath.Join(dir, "supervisor.log")
	stdoutPath := filepath.Join(dir, "stdout.log")
	stderrPath := filepath.Join(dir, "stderr.log")
	if err := os.WriteFile(supervisorPath, []byte("supervisor logs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdoutPath, []byte("stdout content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrPath, []byte("stderr content"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	req := store.RunRequest{ID: "req-1", Workflow: "sample", InputsJSON: `{"token":"secret-input-json"}`, Source: "test", CreatedAt: now}
	run := store.WorkflowRun{ID: "run-1", RequestID: req.ID, Workflow: "sample", DefinitionHash: "sha256:test", DefinitionYAML: "# definition secret prompt\nsteps:\n  - id: plan\n  - id: review\n", InputsJSON: req.InputsJSON, Repo: "/repo", Branch: "po/sample", WorktreePath: "/worktree", ArtifactRoot: "/artifacts/run-1", State: store.StateRunning, SupervisorPID: 123, SupervisorLogPath: supervisorPath, CleanupStatus: "not_requested", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateRunRequestWithWorkflowRun(context.Background(), req, run); err != nil {
		t.Fatal(err)
	}
	step := store.StepRun{WorkflowRunID: run.ID, StepID: "plan", Agent: "planner", ExecutionIndex: 0, State: store.StateRunning, PDTaskID: "pd-task-1", PDRunID: "pd-run-1", PDStdoutPath: stdoutPath, PDStderrPath: stderrPath, PDEventsPath: filepath.Join(dir, "events.jsonl"), StartedAt: now, UpdatedAt: now}
	artifact := store.Artifact{WorkflowRunID: run.ID, StepID: step.StepID, Name: "report", RelativePath: "report.md", AbsolutePath: "/artifacts/run-1/report.md", Required: true, Exists: false, UpdatedAt: now}
	if err := st.CreateStepRun(context.Background(), step, []store.Artifact{artifact}); err != nil {
		t.Fatal(err)
	}
	return st, run.ID
}

func assertNotContainsSensitiveRunData(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"secret-input-json", "definition secret prompt", "definition_yaml", "inputs_json"} {
		if strings.Contains(body, secret) {
			t.Fatalf("body contains sensitive data %q: %s", secret, body)
		}
	}
}

var _ Store = failingStore{}
var _ = sql.ErrNoRows
