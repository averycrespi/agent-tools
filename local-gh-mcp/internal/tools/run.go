package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
						"enum":        []string{"queued", "in_progress", "completed", "waiting", "requested", "pending", "cancelled", "failure", "skipped", "stale", "startup_failure", "success", "timed_out", "action_required", "neutral"},
						"description": "Filter by workflow run status.",
					},
					"workflow": map[string]any{
						"type":        "string",
						"description": "Filter by workflow name",
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
			Description: "View workflow run details. With log_failed=false (default), returns structured markdown: run header + per-job status list. With log_failed=true, returns raw concatenated logs for failed jobs — useful for debugging failures.",
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
				},
				Required: []string{"owner", "repo", "run_id"},
			},
		},
		{
			Name:        "gh_rerun",
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
	}
}

func (h *Handler) handleListRuns(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	opts := gh.ListRunsOpts{
		Branch:   stringFromArgs(args, "branch"),
		Status:   stringFromArgs(args, "status"),
		Workflow: stringFromArgs(args, "workflow"),
		Limit:    intFromArgs(args, "limit"),
	}
	out, err := h.gh.ListRuns(ctx, owner, repo, opts)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var items []format.RunListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse run list JSON: %v", err)), nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, format.FormatRunListItem(item))
	}
	if len(lines) == 0 {
		return gomcp.NewToolResultText("No workflow runs found."), nil
	}
	return gomcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func (h *Handler) handleViewRun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID := stringFromArgs(args, "run_id")
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	logFailed := boolFromArgs(args, "log_failed")
	out, err := h.gh.ViewRun(ctx, owner, repo, runID, logFailed)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	if logFailed {
		return gomcp.NewToolResultText(out), nil
	}
	var run format.RunView
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("failed to parse run JSON: %v", err)), nil
	}
	return gomcp.NewToolResultText(format.FormatRunView(run)), nil
}

func (h *Handler) handleRerun(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	owner, repo, errResult := requireOwnerRepo(args)
	if errResult != nil {
		return errResult, nil
	}
	runID := stringFromArgs(args, "run_id")
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
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
	runID := stringFromArgs(args, "run_id")
	if runID == "" {
		return gomcp.NewToolResultError("run_id is required"), nil
	}
	out, err := h.gh.CancelRun(ctx, owner, repo, runID)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(out), nil
}
