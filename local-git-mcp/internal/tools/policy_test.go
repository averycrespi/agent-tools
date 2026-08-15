package tools

import (
	"context"
	"sort"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validPushArguments(repoPath string) map[string]any {
	return map[string]any{
		"repo_path":       repoPath,
		"remote":          "origin",
		"remote_url":      "git@github.com:acme/repo.git",
		"source_ref":      "refs/heads/topic",
		"destination_ref": "refs/heads/main",
		"force":           false,
	}
}

func toolByName(t *testing.T, handler *Handler, name string) gomcp.Tool {
	t.Helper()
	for _, tool := range handler.Tools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return gomcp.Tool{}
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestEveryToolSchemaRejectsAdditionalProperties(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	for _, tool := range h.Tools() {
		t.Run(tool.Name, func(t *testing.T) {
			assert.Equal(t, false, tool.InputSchema.AdditionalProperties)
		})
	}
}

func TestRemoteOperationSchemasExposePolicyArguments(t *testing.T) {
	h := NewHandler(&mockGitClient{})

	push := toolByName(t, h, "push")
	assert.ElementsMatch(t, []string{"repo_path", "remote", "remote_url", "source_ref", "destination_ref", "force"}, push.InputSchema.Required)
	assert.Equal(t, []string{"destination_ref", "force", "remote", "remote_url", "repo_path", "source_ref"}, sortedKeys(push.InputSchema.Properties))
	assert.NotContains(t, push.InputSchema.Properties, "refspec")

	pull := toolByName(t, h, "pull")
	assert.ElementsMatch(t, []string{"repo_path", "remote", "remote_url"}, pull.InputSchema.Required)
	assert.Equal(t, []string{"branch", "rebase", "remote", "remote_url", "repo_path"}, sortedKeys(pull.InputSchema.Properties))

	fetch := toolByName(t, h, "fetch")
	assert.ElementsMatch(t, []string{"repo_path", "remote", "remote_url"}, fetch.InputSchema.Required)
	assert.Equal(t, []string{"refspec", "remote", "remote_url", "repo_path"}, sortedKeys(fetch.InputSchema.Properties))
}

func TestPushHandler_AcceptsAndForwardsExplicitFalse(t *testing.T) {
	called := false
	h := NewHandler(&mockGitClient{
		pushFunc: func(ctx context.Context, repoPath, remote, remoteURL, sourceRef, destinationRef string, force bool) (string, error) {
			called = true
			assert.Equal(t, "/repo", repoPath)
			assert.Equal(t, "origin", remote)
			assert.Equal(t, "git@github.com:acme/repo.git", remoteURL)
			assert.Equal(t, "refs/heads/topic", sourceRef)
			assert.Equal(t, "refs/heads/main", destinationRef)
			assert.False(t, force)
			return "ok", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "push"
	req.Params.Arguments = validPushArguments("/repo")

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.True(t, called)
}

func TestPushHandler_RejectsMissingOrWronglyTypedRequiredValuesBeforeRepoValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing remote", mutate: func(args map[string]any) { delete(args, "remote") }},
		{name: "wrong remote", mutate: func(args map[string]any) { args["remote"] = 1 }},
		{name: "missing remote_url", mutate: func(args map[string]any) { delete(args, "remote_url") }},
		{name: "wrong remote_url", mutate: func(args map[string]any) { args["remote_url"] = false }},
		{name: "missing source_ref", mutate: func(args map[string]any) { delete(args, "source_ref") }},
		{name: "wrong source_ref", mutate: func(args map[string]any) { args["source_ref"] = 1 }},
		{name: "missing destination_ref", mutate: func(args map[string]any) { delete(args, "destination_ref") }},
		{name: "wrong destination_ref", mutate: func(args map[string]any) { args["destination_ref"] = false }},
		{name: "missing force", mutate: func(args map[string]any) { delete(args, "force") }},
		{name: "wrong force", mutate: func(args map[string]any) { args["force"] = "false" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validated := false
			h := NewHandler(&mockGitClient{
				validateRepoFunc: func(ctx context.Context, repoPath string) (string, error) {
					validated = true
					return repoPath, nil
				},
			})
			args := validPushArguments("/repo")
			tt.mutate(args)
			req := gomcp.CallToolRequest{}
			req.Params.Name = "push"
			req.Params.Arguments = args

			result, err := h.Handle(context.Background(), req)

			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.False(t, validated)
		})
	}
}

func TestFetchAndPullHandlers_ForwardRequiredRemoteURLAndOptions(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		h := NewHandler(&mockGitClient{
			fetchFunc: func(ctx context.Context, repoPath, remote, remoteURL, refspec string) (string, error) {
				assert.Equal(t, "/repo", repoPath)
				assert.Equal(t, "upstream", remote)
				assert.Equal(t, "ssh://example.com/repo.git", remoteURL)
				assert.Equal(t, "refs/heads/main", refspec)
				return "ok", nil
			},
		})
		req := gomcp.CallToolRequest{}
		req.Params.Name = "fetch"
		req.Params.Arguments = map[string]any{
			"repo_path":  "/repo",
			"remote":     "upstream",
			"remote_url": "ssh://example.com/repo.git",
			"refspec":    "refs/heads/main",
		}

		result, err := h.Handle(context.Background(), req)

		require.NoError(t, err)
		assert.False(t, result.IsError)
	})

	t.Run("pull", func(t *testing.T) {
		h := NewHandler(&mockGitClient{
			pullFunc: func(ctx context.Context, repoPath, remote, remoteURL, branch string, rebase bool) (string, error) {
				assert.Equal(t, "/repo", repoPath)
				assert.Equal(t, "upstream", remote)
				assert.Equal(t, "ssh://example.com/repo.git", remoteURL)
				assert.Equal(t, "main", branch)
				assert.True(t, rebase)
				return "ok", nil
			},
		})
		req := gomcp.CallToolRequest{}
		req.Params.Name = "pull"
		req.Params.Arguments = map[string]any{
			"repo_path":  "/repo",
			"remote":     "upstream",
			"remote_url": "ssh://example.com/repo.git",
			"branch":     "main",
			"rebase":     true,
		}

		result, err := h.Handle(context.Background(), req)

		require.NoError(t, err)
		assert.False(t, result.IsError)
	})
}

func TestFetchAndPullHandlers_RequireTypedRemoteAssertionsBeforeRepoValidation(t *testing.T) {
	for _, toolName := range []string{"fetch", "pull"} {
		for _, field := range []string{"remote", "remote_url"} {
			t.Run(toolName+" missing "+field, func(t *testing.T) {
				validated := false
				h := NewHandler(&mockGitClient{
					validateRepoFunc: func(ctx context.Context, repoPath string) (string, error) {
						validated = true
						return repoPath, nil
					},
				})
				args := map[string]any{
					"repo_path":  "/repo",
					"remote":     "origin",
					"remote_url": "ssh://example.com/repo.git",
				}
				delete(args, field)
				req := gomcp.CallToolRequest{}
				req.Params.Name = toolName
				req.Params.Arguments = args

				result, err := h.Handle(context.Background(), req)

				require.NoError(t, err)
				assert.True(t, result.IsError)
				assert.False(t, validated)
			})

			t.Run(toolName+" wrong "+field, func(t *testing.T) {
				validated := false
				h := NewHandler(&mockGitClient{
					validateRepoFunc: func(ctx context.Context, repoPath string) (string, error) {
						validated = true
						return repoPath, nil
					},
				})
				args := map[string]any{
					"repo_path":  "/repo",
					"remote":     "origin",
					"remote_url": "ssh://example.com/repo.git",
				}
				args[field] = 1
				req := gomcp.CallToolRequest{}
				req.Params.Name = toolName
				req.Params.Arguments = args

				result, err := h.Handle(context.Background(), req)

				require.NoError(t, err)
				assert.True(t, result.IsError)
				assert.False(t, validated)
			})
		}
	}
}
