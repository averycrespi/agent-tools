package pomcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/workflow"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const (
	DefaultLogLimit     = 64 * 1024
	MaxLogLimit         = 1024 * 1024
	DefaultRunListLimit = 100
	MaxRunListLimit     = 1000
)

var annReadLocal = gomcp.ToolAnnotation{
	ReadOnlyHint:  gomcp.ToBoolPtr(true),
	OpenWorldHint: gomcp.ToBoolPtr(false),
}

type Store interface {
	ListWorkflowRunSummariesPage(context.Context, int, int) ([]store.WorkflowRunSummary, error)
	GetWorkflowRunDetail(context.Context, string) (store.WorkflowRunDetail, error)
	WorkflowRunExists(context.Context, string) error
	GetWorkflowRunSupervisorLogPath(context.Context, string) (string, error)
	GetWorkflowStepLogPath(context.Context, string, string, string) (string, error)
}

type Handler struct {
	store       Store
	workflowDir string
}

type WorkflowSummary struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Repo        string               `json:"repo,omitempty"`
	Inputs      []InputSummary       `json:"inputs,omitempty"`
	Agents      []AgentSummary       `json:"agents,omitempty"`
	Steps       []StepSummary        `json:"steps,omitempty"`
	Artifacts   []ArtifactDefinition `json:"artifacts,omitempty"`
	SourcePath  string               `json:"source_path"`
	Valid       bool                 `json:"valid"`
	Error       string               `json:"error,omitempty"`
}

type WorkflowDetail struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Repo        string               `json:"repo,omitempty"`
	Inputs      []InputSummary       `json:"inputs,omitempty"`
	Agents      []AgentSummary       `json:"agents,omitempty"`
	Steps       []StepDetailSummary  `json:"steps,omitempty"`
	Artifacts   []ArtifactDefinition `json:"artifacts,omitempty"`
	SourcePath  string               `json:"source_path"`
	Valid       bool                 `json:"valid"`
	Error       string               `json:"error,omitempty"`
	RawYAML     string               `json:"raw_yaml"`
}

type InputSummary struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  any      `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

type AgentSummary struct {
	Name   string   `json:"name"`
	Model  string   `json:"model,omitempty"`
	Skills []string `json:"skills,omitempty"`
}

type StepSummary struct {
	ID    string   `json:"id"`
	Agent string   `json:"agent"`
	Needs []string `json:"needs,omitempty"`
}

type StepDetailSummary struct {
	ID        string               `json:"id"`
	Agent     string               `json:"agent"`
	Needs     []string             `json:"needs,omitempty"`
	Prompt    string               `json:"prompt"`
	Artifacts []ArtifactDefinition `json:"artifacts,omitempty"`
}

type ArtifactDefinition struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type RunSummary struct {
	ID           string              `json:"id"`
	Workflow     string              `json:"workflow"`
	State        store.State         `json:"state"`
	Repo         string              `json:"repo"`
	Branch       string              `json:"branch"`
	WorktreePath string              `json:"worktree_path"`
	ArtifactRoot string              `json:"artifact_root"`
	Outcome      string              `json:"outcome,omitempty"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
	StepCounts   map[store.State]int `json:"step_counts"`
	StepTotal    int                 `json:"step_total"`
	StepPending  int                 `json:"step_pending"`
	Progress     map[string]any      `json:"progress"`
}

type RunList struct {
	Runs       []RunSummary `json:"runs"`
	Offset     int          `json:"offset"`
	Limit      int          `json:"limit"`
	NextOffset int          `json:"next_offset"`
	HasMore    bool         `json:"has_more"`
}

type RunDetail struct {
	Run       RunDetailSummary `json:"run"`
	Steps     []StepRunView    `json:"steps"`
	Artifacts []ArtifactView   `json:"artifacts"`
}

type RunDetailSummary struct {
	ID                 string      `json:"id"`
	RequestID          string      `json:"request_id"`
	Workflow           string      `json:"workflow"`
	DefinitionHash     string      `json:"definition_hash"`
	Repo               string      `json:"repo"`
	Branch             string      `json:"branch"`
	WorktreePath       string      `json:"worktree_path"`
	ArtifactRoot       string      `json:"artifact_root"`
	State              store.State `json:"state"`
	SupervisorPID      int         `json:"supervisor_pid"`
	SupervisorLogPath  string      `json:"supervisor_log_path"`
	Outcome            string      `json:"outcome,omitempty"`
	CleanupStatus      string      `json:"cleanup_status"`
	CleanupError       string      `json:"cleanup_error,omitempty"`
	CleanupAttemptedAt *time.Time  `json:"cleanup_attempted_at,omitempty"`
	CreatedAt          time.Time   `json:"created_at"`
	UpdatedAt          time.Time   `json:"updated_at"`
	EndedAt            *time.Time  `json:"ended_at,omitempty"`
	StepTotal          int         `json:"step_total"`
	StepPending        int         `json:"step_pending"`
}

type StepRunView struct {
	WorkflowRunID  string      `json:"workflow_run_id"`
	StepID         string      `json:"step_id"`
	Agent          string      `json:"agent"`
	ExecutionIndex int         `json:"execution_index"`
	State          store.State `json:"state"`
	PDTaskID       string      `json:"pd_task_id"`
	PDRunID        string      `json:"pd_run_id"`
	PDStdoutPath   string      `json:"pd_stdout_path"`
	PDStderrPath   string      `json:"pd_stderr_path"`
	PDEventsPath   string      `json:"pd_events_path"`
	Outcome        string      `json:"outcome,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	EndedAt        *time.Time  `json:"ended_at,omitempty"`
}

type ArtifactView struct {
	WorkflowRunID string    `json:"workflow_run_id"`
	Name          string    `json:"name"`
	RelativePath  string    `json:"relative_path"`
	AbsolutePath  string    `json:"absolute_path"`
	Exists        bool      `json:"exists"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type LogWindow struct {
	Stream     string `json:"stream"`
	Path       string `json:"path"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
	Content    string `json:"content"`
}

func NewHandler(st Store, workflowDir string) *Handler {
	return &Handler{store: st, workflowDir: workflowDir}
}

func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{Name: "list_workflows", Description: "List configured Pi Orchestrator workflow-definition summaries without prompt bodies", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{}}},
		{Name: "get_workflow", Description: "Get one validated Pi Orchestrator workflow definition including raw YAML", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workflow": map[string]any{"type": "string", "description": "Workflow name"}}, Required: []string{"workflow"}}},
		{Name: "list_workflow_runs", Description: "List Pi Orchestrator workflow-run summaries from persisted state", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"offset": map[string]any{"type": "number", "description": "Run offset to start listing from (default: 0)"}, "limit": map[string]any{"type": "number", "description": "Maximum runs to return (default: 100)"}}}},
		{Name: "get_workflow_run", Description: "Get one Pi Orchestrator workflow-run detail from persisted state", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"run_id": map[string]any{"type": "string", "description": "Workflow run ID"}}, Required: []string{"run_id"}}},
		{Name: "get_workflow_run_logs", Description: "Read a bounded supervisor log window for a Pi Orchestrator workflow run", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"run_id": map[string]any{"type": "string", "description": "Workflow run ID"}, "offset": map[string]any{"type": "number", "description": "Byte offset to start reading from (default: 0)"}, "limit": map[string]any{"type": "number", "description": "Maximum bytes to read (default: 65536)"}}, Required: []string{"run_id"}}},
		{Name: "get_step_logs", Description: "Read a bounded stdout or stderr log window for one workflow step's backing pd run", Annotations: annReadLocal, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"run_id": map[string]any{"type": "string", "description": "Workflow run ID"}, "step_id": map[string]any{"type": "string", "description": "Workflow step ID"}, "stream": map[string]any{"type": "string", "description": "Log stream to read: stdout or stderr (default: stdout)"}, "offset": map[string]any{"type": "number", "description": "Byte offset to start reading from (default: 0)"}, "limit": map[string]any{"type": "number", "description": "Maximum bytes to read (default: 65536)"}}, Required: []string{"run_id", "step_id"}}},
	}
}

func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
	case "list_workflows":
		return h.listWorkflows()
	case "get_workflow":
		return h.getWorkflow(req.GetArguments())
	case "list_workflow_runs":
		return h.listWorkflowRuns(ctx, req.GetArguments())
	case "get_workflow_run":
		return h.getWorkflowRun(ctx, req.GetArguments())
	case "get_workflow_run_logs":
		return h.getWorkflowRunLogs(ctx, req.GetArguments())
	case "get_step_logs":
		return h.getStepLogs(ctx, req.GetArguments())
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func (h *Handler) listWorkflows() (*gomcp.CallToolResult, error) {
	entries, err := os.ReadDir(h.workflowDir)
	if err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("list workflows: %v", err)), nil
	}
	workflows := make([]WorkflowSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := workflowNameFromFile(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(h.workflowDir, entry.Name())
		def, err := workflow.LoadFile(path)
		if err != nil {
			workflows = append(workflows, WorkflowSummary{Name: name, SourcePath: path, Valid: false, Error: err.Error()})
			continue
		}
		workflows = append(workflows, workflowSummary(def, path, true, ""))
	}
	sort.Slice(workflows, func(i, j int) bool { return workflows[i].Name < workflows[j].Name })
	return jsonResult(map[string]any{"workflows": workflows})
}

func (h *Handler) getWorkflow(args map[string]any) (*gomcp.CallToolResult, error) {
	name, _ := args["workflow"].(string)
	if name == "" {
		return gomcp.NewToolResultError("workflow is required"), nil
	}
	path, err := h.existingWorkflowFilePath(name)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is resolved under the configured workflow definition directory.
	if err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("read workflow %s: %v", path, err)), nil
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	def, err := workflow.LoadBytes(data, stem, path)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	summary := workflowSummary(def, path, true, "")
	return jsonResult(workflowDetail(summary, string(data), workflowStepDetails(def)))
}

func (h *Handler) listWorkflowRuns(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	offset, limit, result := parseRunListArgs(args)
	if result != nil {
		return result, nil
	}
	summaries, err := h.store.ListWorkflowRunSummariesPage(ctx, limit+1, offset)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	hasMore := len(summaries) > limit
	if hasMore {
		summaries = summaries[:limit]
	}
	runs := make([]RunSummary, 0, len(summaries))
	for _, summary := range summaries {
		runs = append(runs, runSummaryView(summary))
	}
	return jsonResult(RunList{Runs: runs, Offset: offset, Limit: limit, NextOffset: offset + len(runs), HasMore: hasMore})
}

func (h *Handler) getWorkflowRun(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	detail, err := h.store.GetWorkflowRunDetail(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("workflow run not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(RunDetail{Run: runDetailSummaryView(detail), Steps: stepRunViews(detail.Steps), Artifacts: artifactViews(detail.Artifacts)})
}

func (h *Handler) getWorkflowRunLogs(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	offset, limit, result := parseLogWindowArgs(args)
	if result != nil {
		return result, nil
	}
	path, err := h.store.GetWorkflowRunSupervisorLogPath(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("workflow run not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	window, err := readLogWindow(path, "supervisor", offset, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(window)
}

func (h *Handler) getStepLogs(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	stepID, _ := args["step_id"].(string)
	if stepID == "" {
		return gomcp.NewToolResultError("step_id is required"), nil
	}
	stream, err := stringArgOrDefault(args, "stream", "stdout")
	if err != nil || (stream != "stdout" && stream != "stderr") {
		return gomcp.NewToolResultError("stream must be stdout or stderr"), nil
	}
	offset, limit, result := parseLogWindowArgs(args)
	if result != nil {
		return result, nil
	}
	if err := h.store.WorkflowRunExists(ctx, runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("workflow run not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	path, err := h.store.GetWorkflowStepLogPath(ctx, runID, stepID, stream)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("workflow step not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	window, err := readLogWindow(path, stream, offset, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(window)
}

func workflowSummary(def *workflow.Definition, path string, valid bool, validationErr string) WorkflowSummary {
	return WorkflowSummary{Name: def.Name, Description: def.Description, Repo: def.Repo, Inputs: inputSummaries(def.Inputs), Agents: agentSummaries(def.Agents), Steps: workflowStepSummaries(def), Artifacts: workflowArtifacts(def), SourcePath: path, Valid: valid, Error: validationErr}
}

func inputSummaries(inputs map[string]workflow.InputSchema) []InputSummary {
	out := make([]InputSummary, 0, len(inputs))
	for name, input := range inputs {
		out = append(out, InputSummary{Name: name, Type: input.Type, Required: input.Required, Default: input.Default, Enum: input.Enum})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func agentSummaries(agents map[string]workflow.Agent) []AgentSummary {
	out := make([]AgentSummary, 0, len(agents))
	for name, agent := range agents {
		out = append(out, AgentSummary{Name: name, Model: agent.Model, Skills: agent.Skills})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func workflowStepSummaries(def *workflow.Definition) []StepSummary {
	out := make([]StepSummary, 0, len(def.Steps))
	for _, step := range def.Steps {
		out = append(out, StepSummary{ID: step.ID, Agent: step.Agent, Needs: step.Needs})
	}
	return out
}

func workflowStepDetails(def *workflow.Definition) []StepDetailSummary {
	out := make([]StepDetailSummary, 0, len(def.Steps))
	for _, step := range def.Steps {
		out = append(out, StepDetailSummary{ID: step.ID, Agent: step.Agent, Needs: step.Needs, Prompt: step.Prompt})
	}
	return out
}

func workflowArtifacts(def *workflow.Definition) []ArtifactDefinition {
	out := make([]ArtifactDefinition, 0, len(def.Artifacts))
	for name, artifact := range def.Artifacts {
		out = append(out, ArtifactDefinition{Name: name, Path: artifact.Path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func workflowDetail(summary WorkflowSummary, rawYAML string, steps []StepDetailSummary) WorkflowDetail {
	return WorkflowDetail{Name: summary.Name, Description: summary.Description, Repo: summary.Repo, Inputs: summary.Inputs, Agents: summary.Agents, Steps: steps, Artifacts: summary.Artifacts, SourcePath: summary.SourcePath, Valid: summary.Valid, Error: summary.Error, RawYAML: rawYAML}
}

func runSummaryView(summary store.WorkflowRunSummary) RunSummary {
	return RunSummary{ID: summary.ID, Workflow: summary.Workflow, State: summary.State, Repo: summary.Repo, Branch: summary.Branch, WorktreePath: summary.WorktreePath, ArtifactRoot: summary.ArtifactRoot, Outcome: summary.Outcome, CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt, StepCounts: summary.StepCounts, StepTotal: summary.StepTotal, StepPending: summary.StepPending, Progress: map[string]any{"total": summary.StepTotal, "pending": summary.StepPending, "counts": summary.StepCounts}}
}

func runDetailSummaryView(detail store.WorkflowRunDetail) RunDetailSummary {
	run := detail.Run
	view := RunDetailSummary{ID: run.ID, RequestID: run.RequestID, Workflow: run.Workflow, DefinitionHash: run.DefinitionHash, Repo: run.Repo, Branch: run.Branch, WorktreePath: run.WorktreePath, ArtifactRoot: run.ArtifactRoot, State: run.State, SupervisorPID: run.SupervisorPID, SupervisorLogPath: run.SupervisorLogPath, Outcome: run.Outcome, CleanupStatus: run.CleanupStatus, CleanupError: run.CleanupError, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, StepTotal: detail.StepTotal, StepPending: detail.StepPending}
	if run.CleanupAttemptedAt.Valid {
		view.CleanupAttemptedAt = &run.CleanupAttemptedAt.Time
	}
	if run.EndedAt.Valid {
		view.EndedAt = &run.EndedAt.Time
	}
	return view
}

func stepRunViews(steps []store.StepRun) []StepRunView {
	out := make([]StepRunView, 0, len(steps))
	for _, step := range steps {
		view := StepRunView{WorkflowRunID: step.WorkflowRunID, StepID: step.StepID, Agent: step.Agent, ExecutionIndex: step.ExecutionIndex, State: step.State, PDTaskID: step.PDTaskID, PDRunID: step.PDRunID, PDStdoutPath: step.PDStdoutPath, PDStderrPath: step.PDStderrPath, PDEventsPath: step.PDEventsPath, Outcome: step.Outcome, StartedAt: step.StartedAt, UpdatedAt: step.UpdatedAt}
		if step.EndedAt.Valid {
			view.EndedAt = &step.EndedAt.Time
		}
		out = append(out, view)
	}
	return out
}

func artifactViews(artifacts []store.Artifact) []ArtifactView {
	out := make([]ArtifactView, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, ArtifactView{WorkflowRunID: artifact.WorkflowRunID, Name: artifact.Name, RelativePath: artifact.RelativePath, AbsolutePath: artifact.AbsolutePath, Exists: artifact.Exists, UpdatedAt: artifact.UpdatedAt})
	}
	return out
}

func parseRunListArgs(args map[string]any) (int, int, *gomcp.CallToolResult) {
	offset, err := intOrDefault(args, "offset", 0)
	if err != nil || offset < 0 {
		return 0, 0, gomcp.NewToolResultError("offset must be a non-negative integer")
	}
	limit, err := intOrDefault(args, "limit", DefaultRunListLimit)
	if err != nil || limit < 0 {
		return 0, 0, gomcp.NewToolResultError("limit must be a non-negative integer")
	}
	if limit > MaxRunListLimit {
		return 0, 0, gomcp.NewToolResultError(fmt.Sprintf("limit must be less than or equal to %d", MaxRunListLimit))
	}
	return offset, limit, nil
}

func parseLogWindowArgs(args map[string]any) (int64, int, *gomcp.CallToolResult) {
	offset, err := int64OrDefault(args, "offset", 0)
	if err != nil || offset < 0 {
		return 0, 0, gomcp.NewToolResultError("offset must be a non-negative integer")
	}
	limit, err := intOrDefault(args, "limit", DefaultLogLimit)
	if err != nil || limit < 0 {
		return 0, 0, gomcp.NewToolResultError("limit must be a non-negative integer")
	}
	if limit > MaxLogLimit {
		return 0, 0, gomcp.NewToolResultError(fmt.Sprintf("limit must be less than or equal to %d", MaxLogLimit))
	}
	return offset, limit, nil
}

func readLogWindow(path string, stream string, offset int64, limit int) (LogWindow, error) {
	if path == "" {
		return LogWindow{}, fmt.Errorf("log path is empty")
	}
	file, err := os.Open(path) //nolint:gosec // path comes from persisted local workflow run state.
	if err != nil {
		return LogWindow{}, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return LogWindow{}, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return LogWindow{}, err
	}
	buf := make([]byte, limit)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogWindow{}, err
	}
	next := offset + int64(n)
	return LogWindow{Stream: stream, Path: path, Offset: offset, NextOffset: next, Size: info.Size(), Truncated: next < info.Size(), Content: string(buf[:n])}, nil
}

func (h *Handler) existingWorkflowFilePath(name string) (string, error) {
	path, err := h.workflowFilePath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil || filepath.Ext(name) != "" {
		return path, err
	}
	ymlPath := strings.TrimSuffix(path, ".yaml") + ".yml"
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath, nil
	}
	return path, nil
}

func (h *Handler) workflowFilePath(name string) (string, error) {
	if strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("workflow name must not contain path separators: %s", name)
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" || stem == "." || stem == ".." {
		return "", fmt.Errorf("workflow name is invalid: %s", name)
	}
	if filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".yml" {
		return filepath.Join(h.workflowDir, name), nil
	}
	return filepath.Join(h.workflowDir, name+".yaml"), nil
}

func workflowNameFromFile(name string) (string, bool) {
	ext := filepath.Ext(name)
	if ext != ".yaml" && ext != ".yml" {
		return "", false
	}
	return strings.TrimSuffix(name, ext), true
}

func jsonResult(payload any) (*gomcp.CallToolResult, error) {
	out, err := json.Marshal(payload)
	if err != nil {
		return gomcp.NewToolResultError("encode failed"), nil
	}
	return gomcp.NewToolResultText(string(out)), nil
}

func stringArgOrDefault(args map[string]any, key, defaultVal string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return defaultVal, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if text == "" {
		return defaultVal, nil
	}
	return text, nil
}

func intOrDefault(args map[string]any, key string, defaultVal int) (int, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return defaultVal, nil
	}
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

func int64OrDefault(args map[string]any, key string, defaultVal int64) (int64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return defaultVal, nil
	}
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		if v != float64(int64(v)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int64(v), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}
