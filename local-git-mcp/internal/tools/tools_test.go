package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/git"
)

type mockGitClient struct {
	validateRepoFunc   func(repoPath string) error
	pushFunc           func(repoPath, remote, refspec string, force bool) (string, error)
	pullFunc           func(repoPath, remote, branch string, rebase bool) (string, error)
	fetchFunc          func(repoPath, remote, refspec string) (string, error)
	listRemoteRefsFunc func(repoPath, remote string) ([]git.Ref, error)
	listRemotesFunc    func(repoPath string) ([]git.Remote, error)
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

func (m *mockGitClient) ListRemoteRefs(repoPath, remote string) ([]git.Ref, error) {
	if m.listRemoteRefsFunc != nil {
		return m.listRemoteRefsFunc(repoPath, remote)
	}
	return nil, nil
}

func (m *mockGitClient) ListRemotes(repoPath string) ([]git.Remote, error) {
	if m.listRemotesFunc != nil {
		return m.listRemotesFunc(repoPath)
	}
	return nil, nil
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

func TestListRemotesHandler_Success(t *testing.T) {
	h := NewHandler(&mockGitClient{
		listRemotesFunc: func(repoPath string) ([]git.Remote, error) {
			return []git.Remote{
				{Name: "origin", FetchURL: "git@github.com:user/repo.git", PushURL: "git@github.com:user/repo.git"},
			}, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_list_remotes"
	req.Params.Arguments = map[string]any{"repo_path": "/repo"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	var remotes []git.Remote
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &remotes))
	assert.Equal(t, "origin", remotes[0].Name)
	assert.Equal(t, "git@github.com:user/repo.git", remotes[0].FetchURL)
}

func TestListRemoteRefsHandler_Success(t *testing.T) {
	h := NewHandler(&mockGitClient{
		listRemoteRefsFunc: func(repoPath, remote string) ([]git.Ref, error) {
			return []git.Ref{
				{SHA: "abc123", Ref: "refs/heads/main"},
			}, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "git_list_remote_refs"
	req.Params.Arguments = map[string]any{"repo_path": "/repo"}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	var refs []git.Ref
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &refs))
	assert.Equal(t, "abc123", refs[0].SHA)
	assert.Equal(t, "refs/heads/main", refs[0].Ref)
}

func TestAnnotationPresets_Read(t *testing.T) {
	require.NotNil(t, annRead.ReadOnlyHint)
	assert.True(t, *annRead.ReadOnlyHint)
	require.NotNil(t, annRead.OpenWorldHint)
	assert.True(t, *annRead.OpenWorldHint)
	assert.Nil(t, annRead.DestructiveHint)
	assert.Nil(t, annRead.IdempotentHint)
}

func TestAnnotationPresets_ReadLocal(t *testing.T) {
	require.NotNil(t, annReadLocal.ReadOnlyHint)
	assert.True(t, *annReadLocal.ReadOnlyHint)
	require.NotNil(t, annReadLocal.OpenWorldHint)
	assert.False(t, *annReadLocal.OpenWorldHint)
}

func TestAnnotationPresets_Idempotent(t *testing.T) {
	require.NotNil(t, annIdempotent.IdempotentHint)
	assert.True(t, *annIdempotent.IdempotentHint)
	require.NotNil(t, annIdempotent.DestructiveHint)
	assert.False(t, *annIdempotent.DestructiveHint)
	require.NotNil(t, annIdempotent.OpenWorldHint)
	assert.True(t, *annIdempotent.OpenWorldHint)
	assert.Nil(t, annIdempotent.ReadOnlyHint)
}

func TestAnnotationPresets_Additive(t *testing.T) {
	require.NotNil(t, annAdditive.DestructiveHint)
	assert.False(t, *annAdditive.DestructiveHint)
	require.NotNil(t, annAdditive.OpenWorldHint)
	assert.True(t, *annAdditive.OpenWorldHint)
	assert.Nil(t, annAdditive.ReadOnlyHint)
	assert.Nil(t, annAdditive.IdempotentHint)
}

func TestAnnotationPresets_Destructive(t *testing.T) {
	require.NotNil(t, annDestructive.DestructiveHint)
	assert.True(t, *annDestructive.DestructiveHint)
	require.NotNil(t, annDestructive.OpenWorldHint)
	assert.True(t, *annDestructive.OpenWorldHint)
	assert.Nil(t, annDestructive.ReadOnlyHint)
}

func TestEveryToolHasAnnotations(t *testing.T) {
	h := NewHandler(&mockGitClient{})
	tools := h.Tools()
	require.NotEmpty(t, tools)
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			a := tool.Annotations
			hasHint := a.Title != "" ||
				a.ReadOnlyHint != nil ||
				a.DestructiveHint != nil ||
				a.IdempotentHint != nil ||
				a.OpenWorldHint != nil
			assert.Truef(t, hasHint, "tool %s must set at least one annotation hint", tool.Name)
		})
	}
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
