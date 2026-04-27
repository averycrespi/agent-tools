package tools

import (
	"context"
	"encoding/json"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/format"
	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) runTools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "gh_list_runs",
			Description: "List workflow runs for a repository. Filter by branch, status, or workflow to narrow results.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Repository owner",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Filter by branch",
					},
					"status": map[string]any{
						"type":        "string",
						"enum":        []string{"queued", "completed", "in_progress", "requested", "waiting", "action_required", "cancelled", "failure", "neutral", "skipped", "stale", "startup_failure", "success", "timed_out"},
						"description": "Filter by workflow run status.",
					},
					"workflow": map[string]any{
						"type":        "string",
						"description": "Filter by workflow name",
					},
					"actor": map[string]any{
						"type":        "string",
						"description": "Filter by actor login (GitHub username who triggered the run).",
					},
					"event": map[string]any{
						"type":        "string",
						"description": "Filter by triggering event (e.g. push, pull_request, schedule, workflow_dispatch).",
					},
					"limit": map[string]any{
						"type":        "number",
						"default":     30,
						"description": "Max results (default 30, max 100).",
					},
				},
				Required: []string{"owner", "repo"},
			},
		},
		{
			Name:        "gh_view_run",
			Description: "View workflow run details. With log_failed=false (default), returns structured markdown: run header + per-job status list. With log_failed=true, returns the last `tail_lines` lines of the concatenated failed-job logs (default 200, max 5000), then byte-capped at `max_bytes` (default 50000, max 500000). For a single job's full logs, use gh_view_run_job_logs.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Repository owner",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Workflow run ID",
					},
					"log_failed": map[string]any{
						"type":        "boolean",
						"description": "Show failed log output instead of JSON",
					},
					"tail_lines": map[string]any{
						"type":        "number",
						"default":     200,
						"description": "When log_failed=true, return the last N lines (default 200, max 5000). Ignored when log_failed=false.",
					},
					"max_bytes": map[string]any{
						"type":        "number",
						"default":     50000,
						"description": "When log_failed=true, hard byte cap applied after tail_lines (default 50000, max 500000). Truncates on line boundary with `[truncated — showing N of M bytes]`. Ignored when log_failed=false.",
					},
				},
				Required: []string{"owner", "repo", "run_id"},
			},
		},
		{
			Name:        "gh_rerun_run",
			Description: "Rerun a workflow run. Creates a new run attempt from the original commit. Use failed_only=true to rerun only the failed jobs rather than the full workflow.",
			Annotations: annAdditive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Repository owner",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Workflow run ID",
					},
					"failed_only": map[string]any{
						"type":        "boolean",
						"description": "Only re-run failed jobs",
					},
				},
				Required: []string{"owner", "repo", "run_id"},
			},
		},
		{
			Name:        "gh_cancel_run",
			Description: "Cancel an in-progress workflow run. No effect on completed runs.",
			Annotations: annDestructive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Repository owner",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Workflow run ID",
					},
				},
				Required: []string{"owner", "repo", "run_id"},
			},
		},
		{
			Name:        "gh_view_run_job_logs",
			Description: "Fetch logs for a single workflow job by ID. Returns the last `tail_lines` lines (default 200, max 5000), then byte-capped at `max_bytes` (default 50000, max 500000). Complementary to gh_view_run's log_failed=true, which concatenates all failed-job logs. Use gh_view_run first to discover job IDs.",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"owner": map[string]any{
						"type":        "string",
						"description": "Repository owner.",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "Repository name.",
					},
					"job_id": map[string]any{
						"type":        "number",
						"description": "GitHub job ID (obtained from gh_view_run output).",
					},
					"tail_lines": map[string]any{
						"type":        "number",
						"default":     200,
						"description": "Return the last N lines (default 200, max 5000).",
					},
					"max_bytes": map[string]any{
						"type":        "number",
						"default":     50000,
						"description": "Hard byte cap applied after tail_lines (default 50000, max 500000). Truncates on line boundary with `[truncated — showing N of M bytes]`.",
					},
				},
				Required: []string{"owner", "repo", "job_id"},
			},
		},
	}
}

func (h *Handler) handleListRuns(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	status := stringFromArgs(args, "status")
	if errResult := validateEnum("status", status, []string{
		"queued", "completed", "in_progress", "requested", "waiting",
		"action_required", "cancelled", "failure", "neutral", "skipped",
		"stale", "startup_failure", "success", "timed_out",
	}); errResult != nil {
		return errResult, nil
	}
	limit := clampLimit(intFromArgs(args, "limit"))
	opts := gh.ListRunsOpts{
		Branch:   stringFromArgs(args, "branch"),
		Status:   status,
		Workflow: stringFromArgs(args, "workflow"),
		Actor:    stringFromArgs(args, "actor"),
		Event:    stringFromArgs(args, "event"),
		Limit:    limit,
	}
	out, err := h.gh.ListRuns(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.RunListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return parseError("gh_list_runs", err, out), nil
	}
	if len(items) == 0 {
		return gomcp.NewToolResultText("No workflow runs found."), nil
	}
	return gomcp.NewToolResultText(format.FormatRunList(items, limit)), nil
}

func (h *Handler) handleViewRun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID, errResult := requirePositiveIntString(args, "run_id")
	if errResult != nil {
		return errResult, nil
	}
	logFailed := boolFromArgs(args, "log_failed")
	out, err := h.gh.ViewRun(ctx, owner, repo, runID, logFailed)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	if logFailed {
		tail := clampLogTailLines(intFromArgs(args, "tail_lines"))
		maxBytes := clampLogMaxBytes(intFromArgs(args, "max_bytes"))
		return gomcp.NewToolResultText(format.TruncateBytes(tailLines(out, tail), maxBytes)), nil
	}
	var run format.RunView
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		return parseError("gh_view_run", err, out), nil
	}
	return gomcp.NewToolResultText(format.FormatRunView(run)), nil
}

func (h *Handler) handleRerunRun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID, errResult := requirePositiveIntString(args, "run_id")
	if errResult != nil {
		return errResult, nil
	}
	failedOnly := boolFromArgs(args, "failed_only")
	out, err := h.gh.Rerun(ctx, owner, repo, runID, failedOnly)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleCancelRun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID, errResult := requirePositiveIntString(args, "run_id")
	if errResult != nil {
		return errResult, nil
	}
	out, err := h.gh.CancelRun(ctx, owner, repo, runID)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}

func (h *Handler) handleViewRunJobLogs(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	jobIDInt, errResult := requirePositiveInt(args, "job_id")
	if errResult != nil {
		return errResult, nil
	}
	tail := clampLogTailLines(intFromArgs(args, "tail_lines"))
	maxBytes := clampLogMaxBytes(intFromArgs(args, "max_bytes"))
	out, err := h.gh.ViewRunJobLog(ctx, owner, repo, int64(jobIDInt), tail)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(format.TruncateBytes(out, maxBytes)), nil
}
