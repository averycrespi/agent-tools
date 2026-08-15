package tools

import (
	"context"
	"encoding/json"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
)

// Annotation presets used across all tool definitions.
var (
	// Read tools that touch a remote.
	annRead = gomcp.ToolAnnotation{
		ReadOnlyHint:  gomcp.ToBoolPtr(true),
		OpenWorldHint: gomcp.ToBoolPtr(true),
	}
	// Read tools that only touch local repo state.
	annReadLocal = gomcp.ToolAnnotation{
		ReadOnlyHint:  gomcp.ToBoolPtr(true),
		OpenWorldHint: gomcp.ToBoolPtr(false),
	}
	// Idempotent writes: repeated calls with same args converge to the same local state.
	annIdempotent = gomcp.ToolAnnotation{
		IdempotentHint:  gomcp.ToBoolPtr(true),
		DestructiveHint: gomcp.ToBoolPtr(false),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
	// Additive writes: mutate state, not destructive.
	annAdditive = gomcp.ToolAnnotation{
		DestructiveHint: gomcp.ToBoolPtr(false),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
	// Destructive: rewrites or removes state in non-trivially-reversible ways.
	annDestructive = gomcp.ToolAnnotation{
		DestructiveHint: gomcp.ToBoolPtr(true),
		OpenWorldHint:   gomcp.ToBoolPtr(true),
	}
)

// GitClient defines the git operations needed by MCP tool handlers.
type GitClient interface {
	ValidateRepo(ctx context.Context, repoPath string) (string, error)
	CloneGitHubRepo(ctx context.Context, repository, destinationDir string) (string, error)
	Push(ctx context.Context, repoPath, remote, remoteURL, sourceRef, destinationRef string, force bool) (string, error)
	Pull(ctx context.Context, repoPath, remote, remoteURL, branch string, rebase bool) (string, error)
	Fetch(ctx context.Context, repoPath, remote, remoteURL, refspec string) (string, error)
	ListRemoteRefs(ctx context.Context, repoPath, remote string) ([]git.Ref, error)
	ListRemotes(ctx context.Context, repoPath string) ([]git.Remote, error)
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
			Name:        "push",
			Description: "Push one structured ref update through a verified configured remote",
			Annotations: annDestructive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path":       stringProperty("Absolute path to the git repository"),
					"remote":          stringProperty("Configured remote name"),
					"remote_url":      stringProperty("Exact effective push URL asserted for policy verification"),
					"source_ref":      stringProperty("Fully qualified source ref under refs/heads/ or refs/tags/"),
					"destination_ref": stringProperty("Fully qualified destination ref under refs/heads/ or refs/tags/"),
					"force": map[string]any{
						"type":        "boolean",
						"description": "Use --force-with-lease when true",
					},
				},
				Required:             []string{"repo_path", "remote", "remote_url", "source_ref", "destination_ref", "force"},
				AdditionalProperties: false,
			},
		},
		{
			Name:        "pull",
			Description: "Pull through a verified configured remote",
			Annotations: annAdditive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path":  stringProperty("Absolute path to the git repository"),
					"remote":     stringProperty("Configured remote name"),
					"remote_url": stringProperty("Exact effective fetch URL asserted for policy verification"),
					"branch":     stringProperty("Branch name to pull"),
					"rebase": map[string]any{
						"type":        "boolean",
						"description": "Use --rebase instead of merge",
					},
				},
				Required:             []string{"repo_path", "remote", "remote_url"},
				AdditionalProperties: false,
			},
		},
		{
			Name:        "fetch",
			Description: "Fetch through a verified configured remote without merging",
			Annotations: annIdempotent,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path":  stringProperty("Absolute path to the git repository"),
					"remote":     stringProperty("Configured remote name"),
					"remote_url": stringProperty("Exact effective fetch URL asserted for policy verification"),
					"refspec":    stringProperty("Optional refspec to fetch"),
				},
				Required:             []string{"repo_path", "remote", "remote_url"},
				AdditionalProperties: false,
			},
		},
		{
			Name:        "clone_github_repo",
			Description: "Clone a GitHub repository over SSH into an allowed destination directory",
			Annotations: annAdditive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repository":      stringProperty("GitHub repository in owner/repo form"),
					"destination_dir": stringProperty("Absolute path to an allowed parent directory for the clone"),
				},
				Required:             []string{"repository", "destination_dir"},
				AdditionalProperties: false,
			},
		},
		{
			Name:        "list_remote_refs",
			Description: "List refs (branches, tags) on a remote",
			Annotations: annRead,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": stringProperty("Absolute path to the git repository"),
					"remote":    stringProperty("Remote name (default: origin)"),
				},
				Required:             []string{"repo_path"},
				AdditionalProperties: false,
			},
		},
		{
			Name:        "list_remotes",
			Description: "List configured remotes and their URLs",
			Annotations: annReadLocal,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"repo_path": stringProperty("Absolute path to the git repository"),
				},
				Required:             []string{"repo_path"},
				AdditionalProperties: false,
			},
		},
	}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// Handle dispatches an MCP tool call to the appropriate git operation.
func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()

	if req.Params.Name == "clone_github_repo" {
		repository, err := requiredString(args, "repository")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		destinationDir, err := requiredString(args, "destination_dir")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		repoPath, err := h.git.CloneGitHubRepo(ctx, repository, destinationDir)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(git.CloneGitHubResult{RepoPath: repoPath})
		return gomcp.NewToolResultText(string(out)), nil
	}

	repoPath, err := requiredString(args, "repo_path")
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}

	switch req.Params.Name {
	case "push":
		remote, err := requiredString(args, "remote")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		remoteURL, err := requiredString(args, "remote_url")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		sourceRef, err := requiredString(args, "source_ref")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		destinationRef, err := requiredString(args, "destination_ref")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		force, err := requiredBool(args, "force")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		validatedRepoPath, result := h.validateRepo(ctx, repoPath)
		if result != nil {
			return result, nil
		}
		out, err := h.git.Push(ctx, validatedRepoPath, remote, remoteURL, sourceRef, destinationRef, force)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "pull":
		remote, remoteURL, result := requiredRemoteAssertion(args)
		if result != nil {
			return result, nil
		}
		branch, _ := args["branch"].(string)
		rebase, _ := args["rebase"].(bool)
		validatedRepoPath, result := h.validateRepo(ctx, repoPath)
		if result != nil {
			return result, nil
		}
		out, err := h.git.Pull(ctx, validatedRepoPath, remote, remoteURL, branch, rebase)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "fetch":
		remote, remoteURL, result := requiredRemoteAssertion(args)
		if result != nil {
			return result, nil
		}
		refspec, _ := args["refspec"].(string)
		validatedRepoPath, result := h.validateRepo(ctx, repoPath)
		if result != nil {
			return result, nil
		}
		out, err := h.git.Fetch(ctx, validatedRepoPath, remote, remoteURL, refspec)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(out), nil

	case "list_remote_refs":
		validatedRepoPath, result := h.validateRepo(ctx, repoPath)
		if result != nil {
			return result, nil
		}
		remote := stringOrDefault(args, "remote", "origin")
		refs, err := h.git.ListRemoteRefs(ctx, validatedRepoPath, remote)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(refs)
		return gomcp.NewToolResultText(string(out)), nil

	case "list_remotes":
		validatedRepoPath, result := h.validateRepo(ctx, repoPath)
		if result != nil {
			return result, nil
		}
		remotes, err := h.git.ListRemotes(ctx, validatedRepoPath)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		out, _ := json.Marshal(remotes)
		return gomcp.NewToolResultText(string(out)), nil

	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func (h *Handler) validateRepo(ctx context.Context, repoPath string) (string, *gomcp.CallToolResult) {
	validatedRepoPath, err := h.git.ValidateRepo(ctx, repoPath)
	if err != nil {
		return "", gomcp.NewToolResultError(err.Error())
	}
	return validatedRepoPath, nil
}

func requiredRemoteAssertion(args map[string]any) (string, string, *gomcp.CallToolResult) {
	remote, err := requiredString(args, "remote")
	if err != nil {
		return "", "", gomcp.NewToolResultError(err.Error())
	}
	remoteURL, err := requiredString(args, "remote_url")
	if err != nil {
		return "", "", gomcp.NewToolResultError(err.Error())
	}
	return remote, remoteURL, nil
}

func requiredString(args map[string]any, key string) (string, error) {
	value, ok := args[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required and must be a nonempty string", key)
	}
	return value, nil
}

func requiredBool(args map[string]any, key string) (bool, error) {
	value, ok := args[key].(bool)
	if !ok {
		return false, fmt.Errorf("%s is required and must be a boolean", key)
	}
	return value, nil
}

func stringOrDefault(args map[string]any, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
