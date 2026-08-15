package main

import (
	"context"
	"encoding/json"
	"testing"

	localgit "github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
	"github.com/averycrespi/agent-tools/local-git-mcp/internal/tools"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type serverTestGitClient struct {
	validateCalled  bool
	operationCalled bool
}

func (m *serverTestGitClient) ValidateRepo(_ context.Context, repoPath string) (string, error) {
	m.validateCalled = true
	return repoPath, nil
}

func (m *serverTestGitClient) CloneGitHubRepo(context.Context, string, string) (string, error) {
	m.operationCalled = true
	return "/repo", nil
}

func (m *serverTestGitClient) Push(context.Context, string, string, string, string, string, bool) (string, error) {
	m.operationCalled = true
	return "ok", nil
}

func (m *serverTestGitClient) Pull(context.Context, string, string, string, string, bool) (string, error) {
	m.operationCalled = true
	return "ok", nil
}

func (m *serverTestGitClient) Fetch(context.Context, string, string, string, string) (string, error) {
	m.operationCalled = true
	return "ok", nil
}

func (m *serverTestGitClient) ListRemoteRefs(context.Context, string, string) ([]localgit.Ref, error) {
	m.operationCalled = true
	return nil, nil
}

func (m *serverTestGitClient) ListRemotes(context.Context, string) ([]localgit.Remote, error) {
	m.operationCalled = true
	return nil, nil
}

func callServerTool(t *testing.T, client *serverTestGitClient, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	srv := newMCPServer(tools.NewHandler(client))
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	require.NoError(t, err)
	response := srv.HandleMessage(t.Context(), payload)
	jsonResponse, ok := response.(gomcp.JSONRPCResponse)
	require.Truef(t, ok, "expected JSONRPCResponse, got %T", response)
	result, ok := jsonResponse.Result.(*gomcp.CallToolResult)
	require.Truef(t, ok, "expected CallToolResult, got %T", jsonResponse.Result)
	return result
}

func TestServerInputValidationRejectsInvalidArgumentsBeforeHandlerSideEffects(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		args      map[string]any
		wantError string
	}{
		{
			name:      "unknown argument",
			toolName:  "list_remotes",
			args:      map[string]any{"repo_path": "/repo", "policy_hint": "ignored"},
			wantError: "Properties:[policy_hint]",
		},
		{
			name:     "missing required",
			toolName: "push",
			args: map[string]any{
				"repo_path":       "/repo",
				"remote":          "origin",
				"remote_url":      "ssh://example.com/repo.git",
				"source_ref":      "refs/heads/topic",
				"destination_ref": "refs/heads/main",
			},
			wantError: "Missing:[force]",
		},
		{
			name:     "wrong required type",
			toolName: "push",
			args: map[string]any{
				"repo_path":       "/repo",
				"remote":          "origin",
				"remote_url":      "ssh://example.com/repo.git",
				"source_ref":      "refs/heads/topic",
				"destination_ref": "refs/heads/main",
				"force":           "false",
			},
			wantError: "Got:string Want:[boolean]",
		},
		{
			name:     "clone unknown argument",
			toolName: "clone_github_repo",
			args: map[string]any{
				"repository":      "acme/repo",
				"destination_dir": "/work",
				"policy_hint":     true,
			},
			wantError: "Properties:[policy_hint]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &serverTestGitClient{}

			result := callServerTool(t, client, tt.toolName, tt.args)

			assert.True(t, result.IsError)
			require.NotEmpty(t, result.Content)
			content, ok := result.Content[0].(gomcp.TextContent)
			require.Truef(t, ok, "expected TextContent, got %T", result.Content[0])
			assert.Contains(t, content.Text, "input schema validation failed")
			assert.Contains(t, content.Text, tt.wantError)
			assert.False(t, client.validateCalled)
			assert.False(t, client.operationCalled)
		})
	}
}

func TestServerInputValidationAcceptsExplicitFalse(t *testing.T) {
	client := &serverTestGitClient{}

	result := callServerTool(t, client, "push", map[string]any{
		"repo_path":       "/repo",
		"remote":          "origin",
		"remote_url":      "ssh://example.com/repo.git",
		"source_ref":      "refs/heads/topic",
		"destination_ref": "refs/heads/main",
		"force":           false,
	})

	assert.False(t, result.IsError)
	assert.True(t, client.validateCalled)
	assert.True(t, client.operationCalled)
}
