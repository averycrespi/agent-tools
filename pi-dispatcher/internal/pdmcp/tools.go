package pdmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const (
	defaultLogLimit       = 64 * 1024
	maxPiEventsScanBytes  = 512 * 1024
	maxResponsePreviewLen = 4000
)

var annReadLocal = gomcp.ToolAnnotation{
	ReadOnlyHint:  gomcp.ToBoolPtr(true),
	OpenWorldHint: gomcp.ToBoolPtr(false),
}

type Handler struct {
	store *store.Store
}

type TaskSummary struct {
	ID                         string      `json:"id"`
	RepoPath                   string      `json:"repo_path"`
	RepoName                   string      `json:"repo_name"`
	Branch                     string      `json:"branch"`
	WorktreePath               string      `json:"worktree_path"`
	WorktreeCleanupPolicy      string      `json:"worktree_cleanup_policy"`
	WorktreeCreatedByPD        bool        `json:"worktree_created_by_pd"`
	WorktreeCleanupStatus      string      `json:"worktree_cleanup_status"`
	WorktreeCleanupError       string      `json:"worktree_cleanup_error,omitempty"`
	WorktreeCleanupAttemptedAt *time.Time  `json:"worktree_cleanup_attempted_at,omitempty"`
	WorktreeRemovedAt          *time.Time  `json:"worktree_removed_at,omitempty"`
	PromptSource               string      `json:"prompt_source"`
	PromptPreview              string      `json:"prompt_preview"`
	PromptTruncated            bool        `json:"prompt_truncated"`
	Status                     string      `json:"status"`
	CreatedAt                  time.Time   `json:"created_at"`
	UpdatedAt                  time.Time   `json:"updated_at"`
	LatestRun                  *RunSummary `json:"latest_run,omitempty"`
}

type TaskDetail struct {
	Task              TaskSummary `json:"task"`
	LatestRun         *RunSummary `json:"latest_run,omitempty"`
	ResponsePreview   string      `json:"response_preview,omitempty"`
	ResponseTruncated bool        `json:"response_truncated"`
}

type RunSummary struct {
	ID                 string              `json:"id"`
	TaskID             string              `json:"task_id"`
	Attempt            int                 `json:"attempt"`
	SupervisorPID      int                 `json:"supervisor_pid"`
	PiSessionFile      string              `json:"pi_session_file"`
	Status             string              `json:"status"`
	StartedAt          time.Time           `json:"started_at"`
	EndedAt            *time.Time          `json:"ended_at,omitempty"`
	ExitCode           *int64              `json:"exit_code,omitempty"`
	ErrorMessage       string              `json:"error_message"`
	AgentOptions       AgentOptionsSummary `json:"agent_options"`
	EnvVarNames        []string            `json:"env_var_names,omitempty"`
	MaxDurationSeconds int64               `json:"max_duration_seconds,omitempty"`
	ControlSocketPath  string              `json:"control_socket_path"`
	StdoutLogPath      string              `json:"stdout_log_path"`
	StderrLogPath      string              `json:"stderr_log_path"`
	PiEventsPath       string              `json:"pi_events_path"`
}

type AgentOptionsSummary struct {
	Provider                  string   `json:"provider,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Thinking                  string   `json:"thinking,omitempty"`
	Tools                     []string `json:"tools,omitempty"`
	DisableBuiltinTools       bool     `json:"disable_builtin_tools,omitempty"`
	DisableAllTools           bool     `json:"disable_all_tools,omitempty"`
	Extensions                []string `json:"extensions,omitempty"`
	DisableExtensionDiscovery bool     `json:"disable_extension_discovery,omitempty"`
	Skills                    []string `json:"skills,omitempty"`
	DisableSkillDiscovery     bool     `json:"disable_skill_discovery,omitempty"`
	DisableContextFiles       bool     `json:"disable_context_files,omitempty"`
	SessionDir                string   `json:"session_dir,omitempty"`
}

type LogWindow struct {
	Stream     string `json:"stream"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	Content    string `json:"content"`
}

func NewHandler(st *store.Store) *Handler {
	return &Handler{store: st}
}

func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "list_tasks",
			Description: "List Pi Dispatcher task summaries with latest-run metadata",
			Annotations: annReadLocal,
			InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
		},
		{
			Name:        "get_task",
			Description: "Get one Pi Dispatcher task detail with latest-run metadata and latest assistant response preview",
			Annotations: annReadLocal,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"task_id": map[string]any{"type": "string", "description": "Pi Dispatcher task ID"},
				},
				Required: []string{"task_id"},
			},
		},
		{
			Name:        "get_task_logs",
			Description: "Read a bounded stdout or stderr log window for a Pi Dispatcher task's latest run",
			Annotations: annReadLocal,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"task_id": map[string]any{"type": "string", "description": "Pi Dispatcher task ID"},
					"stream":  map[string]any{"type": "string", "description": "Log stream to read: stdout or stderr (default: stdout)"},
					"offset":  map[string]any{"type": "number", "description": "Byte offset to start reading from (default: 0)"},
					"limit":   map[string]any{"type": "number", "description": "Maximum bytes to read (default: 65536)"},
				},
				Required: []string{"task_id"},
			},
		},
	}
}

func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
	case "list_tasks":
		return h.listTasks(ctx)
	case "get_task":
		return h.getTask(ctx, req.GetArguments())
	case "get_task_logs":
		return h.getTaskLogs(ctx, req.GetArguments())
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func (h *Handler) listTasks(ctx context.Context) (*gomcp.CallToolResult, error) {
	summaries, err := h.store.ListTaskSummaries(ctx)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	out := make([]TaskSummary, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, taskSummaryView(summary))
	}
	return jsonResult(map[string]any{"tasks": out})
}

func (h *Handler) getTask(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return gomcp.NewToolResultError("task_id is required"), nil
	}
	task, err := h.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("task not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	view := taskSummaryFromTask(task)
	run, err := h.store.LatestRun(ctx, taskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var latest *RunSummary
	var responsePreview string
	var responseTruncated bool
	if err == nil {
		latest = runSummaryView(run)
		view.LatestRun = latest
		responsePreview, responseTruncated = responsePreviewFromPiEvents(run.PiEventsPath)
	}
	return jsonResult(TaskDetail{Task: view, LatestRun: latest, ResponsePreview: responsePreview, ResponseTruncated: responseTruncated})
}

func (h *Handler) getTaskLogs(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return gomcp.NewToolResultError("task_id is required"), nil
	}
	stream := stringOrDefault(args, "stream", "stdout")
	if stream != "stdout" && stream != "stderr" {
		return gomcp.NewToolResultError("stream must be stdout or stderr"), nil
	}
	offset, err := int64OrDefault(args, "offset", 0)
	if err != nil || offset < 0 {
		return gomcp.NewToolResultError("offset must be a non-negative integer"), nil
	}
	limit, err := intOrDefault(args, "limit", defaultLogLimit)
	if err != nil || limit < 0 {
		return gomcp.NewToolResultError("limit must be a non-negative integer"), nil
	}
	if _, err := h.store.GetTask(ctx, taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("task not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	run, err := h.store.LatestRun(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gomcp.NewToolResultError("task not found"), nil
		}
		return gomcp.NewToolResultError(err.Error()), nil
	}
	path := run.StdoutLogPath
	if stream == "stderr" {
		path = run.StderrLogPath
	}
	window, err := readLogWindow(path, stream, offset, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(window)
}

func taskSummaryView(summary store.TaskSummary) TaskSummary {
	view := taskSummaryFromTask(summary.Task)
	if summary.LatestRun.Valid {
		view.LatestRun = runSummaryView(summary.LatestRun.Run)
	}
	return view
}

func taskSummaryFromTask(task store.Task) TaskSummary {
	view := TaskSummary{ID: task.ID, RepoPath: task.RepoPath, RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, WorktreeCleanupPolicy: string(task.WorktreeCleanupPolicy), WorktreeCreatedByPD: task.WorktreeCreatedByPD, WorktreeCleanupStatus: string(task.WorktreeCleanupStatus), WorktreeCleanupError: task.WorktreeCleanupError, PromptSource: task.PromptSource, PromptPreview: task.PromptPreview, PromptTruncated: promptTruncated(task.Prompt, task.PromptPreview), Status: string(task.Status), CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if task.WorktreeCleanupAttemptedAt.Valid {
		attemptedAt := task.WorktreeCleanupAttemptedAt.Time
		view.WorktreeCleanupAttemptedAt = &attemptedAt
	}
	if task.WorktreeRemovedAt.Valid {
		removedAt := task.WorktreeRemovedAt.Time
		view.WorktreeRemovedAt = &removedAt
	}
	return view
}

func promptTruncated(prompt, preview string) bool {
	normalized := strings.TrimSpace(strings.Join(strings.Fields(prompt), " "))
	return len(normalized) > len(preview)
}

func runSummaryView(run store.Run) *RunSummary {
	view := &RunSummary{ID: run.ID, TaskID: run.TaskID, Attempt: run.Attempt, SupervisorPID: run.SupervisorPID, PiSessionFile: run.PiSessionFile, Status: string(run.Status), StartedAt: run.StartedAt, ErrorMessage: run.ErrorMessage, AgentOptions: decodeAgentOptions(run.AgentOptionsJSON), EnvVarNames: decodeEnvVarNames(run.EnvVarNamesJSON), MaxDurationSeconds: run.MaxDurationSeconds, ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
	if run.EndedAt.Valid {
		ended := run.EndedAt.Time
		view.EndedAt = &ended
	}
	if run.ExitCode.Valid {
		exitCode := run.ExitCode.Int64
		view.ExitCode = &exitCode
	}
	return view
}

func decodeEnvVarNames(data string) []string {
	if data == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(data), &names); err != nil {
		return nil
	}
	return names
}

func decodeAgentOptions(data string) AgentOptionsSummary {
	if data == "" {
		return AgentOptionsSummary{}
	}
	var agent pdconfig.AgentOptions
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return AgentOptionsSummary{}
	}
	return AgentOptionsSummary{Provider: agent.Provider, Model: agent.Model, Thinking: agent.Thinking, Tools: agent.Tools, DisableBuiltinTools: agent.DisableBuiltinTools, DisableAllTools: agent.DisableAllTools, Extensions: agent.Extensions, DisableExtensionDiscovery: agent.DisableExtensionDiscovery, Skills: agent.Skills, DisableSkillDiscovery: agent.DisableSkillDiscovery, DisableContextFiles: agent.DisableContextFiles, SessionDir: agent.SessionDir}
}

func responsePreviewFromPiEvents(piEventsPath string) (string, bool) {
	piEvents, err := readTail(piEventsPath, maxPiEventsScanBytes)
	if err != nil {
		return "", false
	}
	response, ok := lastAssistantResponseFromPiEvents(piEvents)
	if !ok {
		return "", false
	}
	if len(response) <= maxResponsePreviewLen {
		return response, false
	}
	return response[:maxResponsePreviewLen], true
}

func lastAssistantResponseFromPiEvents(data []byte) (string, bool) {
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		text, ok := assistantResponseFromPiEvent([]byte(lines[i]))
		if ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromPiEvent(data []byte) (string, bool) {
	var event struct {
		Message  messagePayload   `json:"message"`
		Messages []messagePayload `json:"messages"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return "", false
	}
	if text, ok := assistantResponseFromMessage(event.Message); ok {
		return text, true
	}
	for i := len(event.Messages) - 1; i >= 0; i-- {
		if text, ok := assistantResponseFromMessage(event.Messages[i]); ok {
			return text, true
		}
	}
	return "", false
}

func assistantResponseFromMessage(message messagePayload) (string, bool) {
	if message.Role != "assistant" {
		return "", false
	}
	text := strings.TrimSpace(messageText(message.Content))
	return text, text != ""
}

type messagePayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func messageText(content json.RawMessage) string {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // path comes from pd-owned task state.
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
		size = limit
	}
	buf := make([]byte, size)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return nil, err
	}
	return buf[:n], nil
}

func readLogWindow(path string, stream string, offset int64, limit int) (LogWindow, error) {
	file, err := os.Open(path) //nolint:gosec // log path comes from pd-owned task state.
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
	buf := make([]byte, limit)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		return LogWindow{}, err
	}
	return LogWindow{Stream: stream, Offset: offset, NextOffset: offset + int64(n), Size: info.Size(), Content: string(buf[:n])}, nil
}

func jsonResult(payload any) (*gomcp.CallToolResult, error) {
	out, err := json.Marshal(payload)
	if err != nil {
		return gomcp.NewToolResultError("encode failed"), nil
	}
	return gomcp.NewToolResultText(string(out)), nil
}

func stringOrDefault(args map[string]any, key, defaultVal string) string {
	if value, ok := args[key].(string); ok && value != "" {
		return value
	}
	return defaultVal
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
