package tools

import (
	"context"
	"encoding/json"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
)

// GitClient defines the git operations needed by MCP tool handlers.
type GitClient interface {
	ValidateRepo(repoPath string) error
	Push(repoPath, remote, refspec string, force bool) (string, error)
	Pull(repoPath, remote, branch string, rebase bool) (string, error)
	Fetch(repoPath, remote, refspec string) (string, error)
	ListRemoteRefs(repoPath, remote string) ([]git.Ref, error)
	ListRemotes(repoPath string) ([]git.Remote, error)
}

// Handler manages MCP tool definitions and dispatches calls to the git client.
type Handler struct {
	git GitClient
}

// NewHandler creates a Handler with the given git client.
func NewHandler(git GitClient) *Handler {
	return &Handler{git: git}
}

// Tools returns the MCP tool definitions.
func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "git_push",
			Description: "Push commits to a remote repository",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"refspec": map[string]any{
						"type":        "string",
						"description": "Refspec to push (e.g., refs/heads/main)",
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "Force push using --force-with-lease",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_pull",
			Description: "Pull from a remote repository",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "Branch name to pull",
					},
					"rebase": map[string]any{
						"type":        "boolean",
						"description": "Use --rebase instead of merge",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_fetch",
			Description: "Fetch from a remote without merging",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
					"refspec": map[string]any{
						"type":        "string",
						"description": "Refspec to fetch",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_list_remote_refs",
			Description: "List refs (branches, tags) on a remote",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
					"remote": map[string]any{
						"type":        "string",
						"description": "Remote name (default: origin)",
					},
				},
				Required: []string{"repo_path"},
			},
		},
		{
			Name:        "git_list_remotes",
			Description: "List configured remotes and their URLs",
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the git repository",
					},
				},
				Required: []string{"repo_path"},
			},
		},
	}
}

// Handle dispatches an MCP tool call to the appropriate git operation.
func (h *Handler) Handle(_ context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()

	repoPath, _ := args["repo_path"].(string)
	if repoPath == "" {
		return gomcp.NewToolResultError("repo_path is required"), nil
	}

	if err := h.git.ValidateRepo(repoPath); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}

	switch req.Params.Name {
	case "git_push":
		remote := stringOrDefault(args, "remote", "origin")
		refspec, _ := args["refspec"].(string)
		force, _ := args["force"].(bool)
		out, err := h.git.Push(repoPath, remote, refspec, force)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_pull":
		remote := stringOrDefault(args, "remote", "origin")
		branch, _ := args["branch"].(string)
		rebase, _ := args["rebase"].(bool)
		out, err := h.git.Pull(repoPath, remote, branch, rebase)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_fetch":
		remote := stringOrDefault(args, "remote", "origin")
		refspec, _ := args["refspec"].(string)
		out, err := h.git.Fetch(repoPath, remote, refspec)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "git_list_remote_refs":
		remote := stringOrDefault(args, "remote", "origin")
		refs, err := h.git.ListRemoteRefs(repoPath, remote)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(refs)
		return gomcp.NewToolResultText(string(out)), nil

	case "git_list_remotes":
		remotes, err := h.git.ListRemotes(repoPath)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(remotes)
		return gomcp.NewToolResultText(string(out)), nil

	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func stringOrDefault(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
