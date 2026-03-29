package tools

import (
	"context"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGHClient struct {
	createPRFunc      func(ctx context.Context, owner, repo string, opts gh.CreatePROpts) (string, error)
	viewPRFunc        func(ctx context.Context, owner, repo string, number int) (string, error)
	listPRsFunc       func(ctx context.Context, owner, repo string, opts gh.ListPROpts) (string, error)
	diffPRFunc        func(ctx context.Context, owner, repo string, number int) (string, error)
	commentPRFunc     func(ctx context.Context, owner, repo string, number int, body string) (string, error)
	reviewPRFunc      func(ctx context.Context, owner, repo string, number int, event, body string) (string, error)
	mergePRFunc       func(ctx context.Context, owner, repo string, number int, opts gh.MergePROpts) (string, error)
	editPRFunc        func(ctx context.Context, owner, repo string, number int, opts gh.EditPROpts) (string, error)
	checkPRFunc       func(ctx context.Context, owner, repo string, number int) (string, error)
	closePRFunc       func(ctx context.Context, owner, repo string, number int, comment string) (string, error)
	viewIssueFunc     func(ctx context.Context, owner, repo string, number int) (string, error)
	listIssuesFunc    func(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error)
	commentIssueFunc   func(ctx context.Context, owner, repo string, number int, body string) (string, error)
	prCommentsFunc     func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	issueCommentsFunc  func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	listRunsFunc       func(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error)
	viewRunFunc       func(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error)
	rerunFunc         func(ctx context.Context, owner, repo string, runID string, failedOnly bool) (string, error)
	cancelRunFunc     func(ctx context.Context, owner, repo string, runID string) (string, error)
	listCachesFunc    func(ctx context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error)
	deleteCacheFunc   func(ctx context.Context, owner, repo string, cacheID string) (string, error)
	searchPRsFunc     func(ctx context.Context, query string, opts gh.SearchPRsOpts) (string, error)
	searchIssuesFunc  func(ctx context.Context, query string, opts gh.SearchIssuesOpts) (string, error)
	searchReposFunc   func(ctx context.Context, query string, opts gh.SearchReposOpts) (string, error)
	searchCodeFunc    func(ctx context.Context, query string, opts gh.SearchCodeOpts) (string, error)
	searchCommitsFunc func(ctx context.Context, query string, opts gh.SearchCommitsOpts) (string, error)
}

func (m *mockGHClient) CreatePR(ctx context.Context, owner, repo string, opts gh.CreatePROpts) (string, error) {
	if m.createPRFunc != nil {
		return m.createPRFunc(ctx, owner, repo, opts)
	}
	return "", nil
}

func (m *mockGHClient) ViewPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.viewPRFunc != nil {
		return m.viewPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) ListPRs(ctx context.Context, owner, repo string, opts gh.ListPROpts) (string, error) {
	if m.listPRsFunc != nil {
		return m.listPRsFunc(ctx, owner, repo, opts)
	}
	return "", nil
}

func (m *mockGHClient) DiffPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.diffPRFunc != nil {
		return m.diffPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) CommentPR(ctx context.Context, owner, repo string, number int, body string) (string, error) {
	if m.commentPRFunc != nil {
		return m.commentPRFunc(ctx, owner, repo, number, body)
	}
	return "", nil
}

func (m *mockGHClient) ReviewPR(ctx context.Context, owner, repo string, number int, event, body string) (string, error) {
	if m.reviewPRFunc != nil {
		return m.reviewPRFunc(ctx, owner, repo, number, event, body)
	}
	return "", nil
}

func (m *mockGHClient) MergePR(ctx context.Context, owner, repo string, number int, opts gh.MergePROpts) (string, error) {
	if m.mergePRFunc != nil {
		return m.mergePRFunc(ctx, owner, repo, number, opts)
	}
	return "", nil
}

func (m *mockGHClient) EditPR(ctx context.Context, owner, repo string, number int, opts gh.EditPROpts) (string, error) {
	if m.editPRFunc != nil {
		return m.editPRFunc(ctx, owner, repo, number, opts)
	}
	return "", nil
}

func (m *mockGHClient) CheckPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.checkPRFunc != nil {
		return m.checkPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) ClosePR(ctx context.Context, owner, repo string, number int, comment string) (string, error) {
	if m.closePRFunc != nil {
		return m.closePRFunc(ctx, owner, repo, number, comment)
	}
	return "", nil
}

func (m *mockGHClient) ViewIssue(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.viewIssueFunc != nil {
		return m.viewIssueFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) ListIssues(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error) {
	if m.listIssuesFunc != nil {
		return m.listIssuesFunc(ctx, owner, repo, opts)
	}
	return "", nil
}

func (m *mockGHClient) CommentIssue(ctx context.Context, owner, repo string, number int, body string) (string, error) {
	if m.commentIssueFunc != nil {
		return m.commentIssueFunc(ctx, owner, repo, number, body)
	}
	return "", nil
}

func (m *mockGHClient) PRComments(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.prCommentsFunc != nil {
		return m.prCommentsFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}

func (m *mockGHClient) IssueComments(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.issueCommentsFunc != nil {
		return m.issueCommentsFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}

func (m *mockGHClient) ListRuns(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error) {
	if m.listRunsFunc != nil {
		return m.listRunsFunc(ctx, owner, repo, opts)
	}
	return "", nil
}

func (m *mockGHClient) ViewRun(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error) {
	if m.viewRunFunc != nil {
		return m.viewRunFunc(ctx, owner, repo, runID, logFailed)
	}
	return "", nil
}

func (m *mockGHClient) Rerun(ctx context.Context, owner, repo string, runID string, failedOnly bool) (string, error) {
	if m.rerunFunc != nil {
		return m.rerunFunc(ctx, owner, repo, runID, failedOnly)
	}
	return "", nil
}

func (m *mockGHClient) CancelRun(ctx context.Context, owner, repo string, runID string) (string, error) {
	if m.cancelRunFunc != nil {
		return m.cancelRunFunc(ctx, owner, repo, runID)
	}
	return "", nil
}

func (m *mockGHClient) ListCaches(ctx context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error) {
	if m.listCachesFunc != nil {
		return m.listCachesFunc(ctx, owner, repo, opts)
	}
	return "", nil
}

func (m *mockGHClient) DeleteCache(ctx context.Context, owner, repo string, cacheID string) (string, error) {
	if m.deleteCacheFunc != nil {
		return m.deleteCacheFunc(ctx, owner, repo, cacheID)
	}
	return "", nil
}

func (m *mockGHClient) SearchPRs(ctx context.Context, query string, opts gh.SearchPRsOpts) (string, error) {
	if m.searchPRsFunc != nil {
		return m.searchPRsFunc(ctx, query, opts)
	}
	return "", nil
}

func (m *mockGHClient) SearchIssues(ctx context.Context, query string, opts gh.SearchIssuesOpts) (string, error) {
	if m.searchIssuesFunc != nil {
		return m.searchIssuesFunc(ctx, query, opts)
	}
	return "", nil
}

func (m *mockGHClient) SearchRepos(ctx context.Context, query string, opts gh.SearchReposOpts) (string, error) {
	if m.searchReposFunc != nil {
		return m.searchReposFunc(ctx, query, opts)
	}
	return "", nil
}

func (m *mockGHClient) SearchCode(ctx context.Context, query string, opts gh.SearchCodeOpts) (string, error) {
	if m.searchCodeFunc != nil {
		return m.searchCodeFunc(ctx, query, opts)
	}
	return "", nil
}

func (m *mockGHClient) SearchCommits(ctx context.Context, query string, opts gh.SearchCommitsOpts) (string, error) {
	if m.searchCommitsFunc != nil {
		return m.searchCommitsFunc(ctx, query, opts)
	}
	return "", nil
}

func TestCreatePR_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		createPRFunc: func(_ context.Context, owner, repo string, opts gh.CreatePROpts) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, "Fix bug", opts.Title)
			assert.Equal(t, "Fixes #1", opts.Body)
			return "https://github.com/octocat/hello-world/pull/1", nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		"title": "Fix bug",
		"body":  "Fixes #1",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestCreatePR_MissingOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"repo":  "hello-world",
		"title": "Fix bug",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreatePR_InvalidOwner(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "octo cat",
		"repo":  "hello-world",
		"title": "Fix bug",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestCreatePR_MissingTitle(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestViewPR_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		viewPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `{"number":42,"title":"Fix bug"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestMergePR_InvalidMethod(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_merge_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(42),
		"method": "fast-forward",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestReviewPR_InvalidEvent(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_review_pr"
	req.Params.Arguments = map[string]any{
		"owner":  "octocat",
		"repo":   "hello-world",
		"number": float64(42),
		"event":  "reject",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_unknown"
	req.Params.Arguments = map[string]any{}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
