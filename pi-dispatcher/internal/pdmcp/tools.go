package pdmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/inspect"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

var annReadLocal = gomcp.ToolAnnotation{
	ReadOnlyHint:  gomcp.ToBoolPtr(true),
	OpenWorldHint: gomcp.ToBoolPtr(false),
}

type Store interface {
	ListTaskSummaries(context.Context) ([]store.TaskSummary, error)
	GetTask(context.Context, string) (store.Task, error)
	LatestRun(context.Context, string) (store.Run, error)
}

type Handler struct {
	store Store
}

type TaskSummary = inspect.TaskSummary
type TaskDetail = inspect.TaskDetail
type RunSummary = inspect.RunSummary
type AgentOptionsSummary = inspect.AgentOptionsSummary
type LogWindow = inspect.LogWindow

func NewHandler(st Store) *Handler {
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
		out = append(out, inspect.TaskSummaryView(summary))
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
	view := inspect.TaskSummaryFromTask(task)
	run, err := h.store.LatestRun(ctx, taskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var latest *RunSummary
	var responsePreview string
	var responseTruncated bool
	if err == nil {
		latest = inspect.RunSummaryView(run)
		view.LatestRun = latest
		responsePreview, responseTruncated = inspect.ResponsePreviewFromPiEvents(run.PiEventsPath)
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
	limit, err := intOrDefault(args, "limit", inspect.DefaultLogLimit)
	if err != nil || limit < 0 {
		return gomcp.NewToolResultError("limit must be a non-negative integer"), nil
	}
	if limit > inspect.MaxLogLimit {
		return gomcp.NewToolResultError(fmt.Sprintf("limit must be less than or equal to %d", inspect.MaxLogLimit)), nil
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
	window, err := inspect.ReadLogWindow(path, stream, offset, limit)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(window)
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
