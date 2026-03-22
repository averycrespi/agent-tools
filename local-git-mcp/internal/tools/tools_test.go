package tools

import (
	"context"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGitClient struct {
	validateRepoFunc   func(repoPath string) error
	pushFunc           func(repoPath, remote, refspec string, force bool) (string, error)
	pullFunc           func(repoPath, remote, branch string, rebase bool) (string, error)
	fetchFunc          func(repoPath, remote, refspec string) (string, error)
	listRemoteRefsFunc func(repoPath, remote string) (string, error)
	listRemotesFunc    func(repoPath string) (string, error)
}

func (m *mockGitClient) ValidateRepo(repoPath string) error {
	if m.validateRepoFunc != nil {
		return m.validateRepoFunc(repoPath)
	}
	return nil
}

func (m *mockGitClient) Push(repoPath, remote, refspec string, force bool) (string, error) {
	if m.pushFunc != nil {
		return m.pushFunc(repoPath, remote, refspec, force)
	}
	return "", nil
}

func (m *mockGitClient) Pull(repoPath, remote, branch string, rebase bool) (string, error) {
	if m.pullFunc != nil {
		return m.pullFunc(repoPath, remote, branch, rebase)
	}
	return "", nil
}

func (m *mockGitClient) Fetch(repoPath, remote, refspec string) (string, error) {
	if m.fetchFunc != nil {
		return m.fetchFunc(repoPath, remote, refspec)
	}
	return "", nil
}

func (m *mockGitClient) ListRemoteRefs(repoPath, remote string) (string, error) {
	if m.listRemoteRefsFunc != nil {
		return m.listRemoteRefsFunc(repoPath, remote)
	}
	return "", nil
}

func (m *mockGitClient) ListRemotes(repoPath string) (string, error) {
	if m.listRemotesFunc != nil {
		return m.listRemotesFunc(repoPath)
	}
	return "", nil
}

func TestToolCount(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	tools := h.Tools()
	assert.Len(t, tools, 5)
}

func TestPushHandler_Success(t *testing.T) {
	h := NewHandler(&mockGitClient{
		pushFunc: func(repoPath, remote, refspec string, force bool) (string, error) {
			assert.Equal(t, "/my/repo", repoPath)
			assert.Equal(t, "origin", remote)
			assert.Equal(t, "", refspec)
			assert.False(t, force)
			return "Everything up-to-date", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{
		"repo_path": "/my/repo",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestPushHandler_ValidationError(t *testing.T) {
	h := NewHandler(&mockGitClient{
		validateRepoFunc: func(repoPath string) error {
			return fmt.Errorf("not a git repository")
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{
		"repo_path": "/bad/path",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestPushHandler_MissingRepoPath(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_push"
	req.Params.Arguments = map[string]any{}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestPullHandler_WithRebase(t *testing.T) {
	h := NewHandler(&mockGitClient{
		pullFunc: func(repoPath, remote, branch string, rebase bool) (string, error) {
			assert.True(t, rebase)
			assert.Equal(t, "main", branch)
			return "Already up to date.", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_pull"
	req.Params.Arguments = map[string]any{
		"repo_path": "/my/repo",
		"branch":    "main",
		"rebase":    true,
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_unknown"
	req.Params.Arguments = map[string]any{"repo_path": "/repo"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
