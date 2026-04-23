package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/gh"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGHClient struct {
	createPRFunc         func(ctx context.Context, owner, repo string, opts gh.CreatePROpts) (string, error)
	viewPRFunc           func(ctx context.Context, owner, repo string, number int) (string, error)
	listPRsFunc          func(ctx context.Context, owner, repo string, opts gh.ListPROpts) (string, error)
	diffPRFunc           func(ctx context.Context, owner, repo string, number int) (string, error)
	commentPRFunc        func(ctx context.Context, owner, repo string, number int, body string) (string, error)
	reviewPRFunc         func(ctx context.Context, owner, repo string, number int, event, body string) (string, error)
	mergePRFunc          func(ctx context.Context, owner, repo string, number int, opts gh.MergePROpts) (string, error)
	editPRFunc           func(ctx context.Context, owner, repo string, number int, opts gh.EditPROpts) (string, error)
	checkPRFunc          func(ctx context.Context, owner, repo string, number int) (string, error)
	closePRFunc          func(ctx context.Context, owner, repo string, number int, comment string) (string, error)
	viewIssueFunc        func(ctx context.Context, owner, repo string, number int) (string, error)
	listIssuesFunc       func(ctx context.Context, owner, repo string, opts gh.ListIssuesOpts) (string, error)
	commentIssueFunc     func(ctx context.Context, owner, repo string, number int, body string) (string, error)
	prCommentsFunc       func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	prReviewsFunc        func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	prReviewCommentsFunc func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	issueCommentsFunc    func(ctx context.Context, owner, repo string, number int, limit int) (string, error)
	listRunsFunc         func(ctx context.Context, owner, repo string, opts gh.ListRunsOpts) (string, error)
	viewRunFunc          func(ctx context.Context, owner, repo string, runID string, logFailed bool) (string, error)
	rerunFunc            func(ctx context.Context, owner, repo string, runID string, failedOnly bool) (string, error)
	cancelRunFunc        func(ctx context.Context, owner, repo string, runID string) (string, error)
	listCachesFunc       func(ctx context.Context, owner, repo string, opts gh.ListCachesOpts) (string, error)
	deleteCacheFunc      func(ctx context.Context, owner, repo string, cacheID string) (string, error)
	searchPRsFunc        func(ctx context.Context, query string, opts gh.SearchPRsOpts) (string, error)
	searchIssuesFunc     func(ctx context.Context, query string, opts gh.SearchIssuesOpts) (string, error)
	searchReposFunc      func(ctx context.Context, query string, opts gh.SearchReposOpts) (string, error)
	searchCodeFunc       func(ctx context.Context, query string, opts gh.SearchCodeOpts) (string, error)
	searchCommitsFunc    func(ctx context.Context, query string, opts gh.SearchCommitsOpts) (string, error)
	viewUserFunc         func(ctx context.Context) (string, error)
	readyPRFunc          func(ctx context.Context, owner, repo string, number int) (string, error)
	draftPRFunc          func(ctx context.Context, owner, repo string, number int) (string, error)
	reopenPRFunc         func(ctx context.Context, owner, repo string, number int) (string, error)
	listPRFilesFunc      func(ctx context.Context, owner, repo string, number, limit int) (string, error)
	listBranchesFunc     func(ctx context.Context, owner, repo string, limit int) (string, error)
	viewRunJobLogFunc    func(ctx context.Context, owner, repo string, jobID int64, tail int) (string, error)
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

func (m *mockGHClient) PRReviews(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.prReviewsFunc != nil {
		return m.prReviewsFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}

func (m *mockGHClient) PRReviewComments(ctx context.Context, owner, repo string, number int, limit int) (string, error) {
	if m.prReviewCommentsFunc != nil {
		return m.prReviewCommentsFunc(ctx, owner, repo, number, limit)
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

func (m *mockGHClient) ViewUser(ctx context.Context) (string, error) {
	if m.viewUserFunc != nil {
		return m.viewUserFunc(ctx)
	}
	return "", nil
}

func (m *mockGHClient) ReadyPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.readyPRFunc != nil {
		return m.readyPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) DraftPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.draftPRFunc != nil {
		return m.draftPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) ReopenPR(ctx context.Context, owner, repo string, number int) (string, error) {
	if m.reopenPRFunc != nil {
		return m.reopenPRFunc(ctx, owner, repo, number)
	}
	return "", nil
}

func (m *mockGHClient) ListPRFiles(ctx context.Context, owner, repo string, number, limit int) (string, error) {
	if m.listPRFilesFunc != nil {
		return m.listPRFilesFunc(ctx, owner, repo, number, limit)
	}
	return "", nil
}

func (m *mockGHClient) ListBranches(ctx context.Context, owner, repo string, limit int) (string, error) {
	if m.listBranchesFunc != nil {
		return m.listBranchesFunc(ctx, owner, repo, limit)
	}
	return "", nil
}

func (m *mockGHClient) ViewRunJobLog(ctx context.Context, owner, repo string, jobID int64, tail int) (string, error) {
	if m.viewRunJobLogFunc != nil {
		return m.viewRunJobLogFunc(ctx, owner, repo, jobID, tail)
	}
	return "", nil
}

func TestGhReadyPR(t *testing.T) {
	var capturedOwner, capturedRepo string
	var capturedNumber int
	mock := &mockGHClient{
		readyPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			capturedOwner, capturedRepo, capturedNumber = owner, repo, number
			return "https://github.com/x/y/pull/42", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_ready_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedOwner != "x" || capturedRepo != "y" || capturedNumber != 42 {
		t.Errorf("client args not threaded: %q/%q/#%d", capturedOwner, capturedRepo, capturedNumber)
	}
	out := textOf(res)
	if !strings.Contains(out, "PR #42 in x/y marked ready for review") {
		t.Errorf("confirmation missing, got: %s", out)
	}
}

func TestGhDraftPR(t *testing.T) {
	mock := &mockGHClient{
		draftPRFunc: func(_ context.Context, _ /*owner*/, _ /*repo*/ string, _ /*number*/ int) (string, error) {
			return "", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_draft_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(7),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(textOf(res), "PR #7 in x/y converted to draft") {
		t.Errorf("confirmation missing, got: %s", textOf(res))
	}
}

func TestGhReopenPR(t *testing.T) {
	mock := &mockGHClient{
		reopenPRFunc: func(_ context.Context, _ /*owner*/, _ /*repo*/ string, _ /*number*/ int) (string, error) {
			return "", nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_reopen_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(99),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(textOf(res), "PR #99 in x/y reopened") {
		t.Errorf("confirmation missing, got: %s", textOf(res))
	}
}

func TestGhReadyPRPassesThroughGhError(t *testing.T) {
	mock := &mockGHClient{
		readyPRFunc: func(_ context.Context, _, _ string, _ int) (string, error) {
			return "", fmt.Errorf("gh pr ready failed: Pull request is already ready for review")
		},
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_ready_pr", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result, got %+v", res)
	}
	if !strings.Contains(textOf(res), "already ready for review") {
		t.Errorf("gh error message not surfaced, got: %s", textOf(res))
	}
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

func TestGhListPRFiles(t *testing.T) {
	mock := &mockGHClient{
		listPRFilesFunc: func(_ context.Context, _, _ string, _ /*number*/ int, _ /*limit*/ int) (string, error) {
			return `[
				{"filename":"a.go","status":"modified","additions":12,"deletions":3},
				{"filename":"b.go","status":"added","additions":5,"deletions":0}
			]`, nil
		},
	}
	h := NewHandler(mock)
	res, err := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_pr_files", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(42),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := textOf(res)
	for _, want := range []string{"`a.go`", "+12/-3", "(modified)", "`b.go`", "+5/-0", "(added)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestGhListPRFilesTruncates(t *testing.T) {
	// Build a 40-file payload; with limit=30 (default), trailer should say "showing 30 of 40".
	parts := make([]string, 40)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"filename":"f%d.go","status":"modified","additions":1,"deletions":0}`, i)
	}
	payload := "[" + strings.Join(parts, ",") + "]"
	mock := &mockGHClient{
		listPRFilesFunc: func(_ context.Context, _, _ string, _, _ int) (string, error) { return payload, nil },
	}
	h := NewHandler(mock)
	res, _ := h.Handle(context.Background(), gomcp.CallToolRequest{
		Params: gomcp.CallToolParams{Name: "gh_list_pr_files", Arguments: map[string]any{
			"owner": "x", "repo": "y", "pr_number": float64(1),
		}},
	})
	if !strings.Contains(textOf(res), "showing 30 of 40") {
		t.Errorf("expected truncation trailer, got: %s", textOf(res))
	}
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
			return `{"number":42,"title":"Fix bug","body":"Fixes it","state":"OPEN","author":{"login":"octocat","is_bot":false},"baseRefName":"main","headRefName":"fix","isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","labels":[],"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z"}`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "# PR #42: Fix bug (OPEN)")
	assert.Contains(t, text, "@octocat")
	assert.Contains(t, text, "main <- fix")
	assert.Contains(t, text, "Fixes it")
}

func TestMergePR_InvalidMethod(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_merge_pr"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
		"method":    "fast-forward",
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
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
		"event":     "reject",
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

func TestListPRComments_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		prCommentsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `[{"author":{"login":"reviewer"},"authorAssociation":"MEMBER","body":"LGTM","createdAt":"2025-01-01T00:00:00Z","isMinimized":false,"minimizedReason":""}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_comments"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Comments (1)")
	assert.Contains(t, text, "@reviewer [MEMBER]")
	assert.Contains(t, text, "LGTM")
}

func TestListPRReviews_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		prReviewsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `[{"author":{"login":"alice"},"authorAssociation":"MEMBER","body":"LGTM","state":"APPROVED","submittedAt":"2026-04-10T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_reviews"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Reviews (1)")
	assert.Contains(t, text, "@alice [MEMBER] — APPROVED")
	assert.Contains(t, text, "LGTM")
}

func TestListPRReviews_MissingNumber(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_reviews"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestListPRReviewComments_Success(t *testing.T) {
	h := NewHandler(&mockGHClient{
		prReviewCommentsFunc: func(_ context.Context, owner, repo string, number int, limit int) (string, error) {
			assert.Equal(t, "octocat", owner)
			assert.Equal(t, "hello-world", repo)
			assert.Equal(t, 42, number)
			return `[{"id":1,"in_reply_to_id":0,"pull_request_review_id":100,"user":{"login":"alice","type":"User"},"body":"nil-check","path":"src/foo.go","line":42,"original_line":42,"side":"RIGHT","diff_hunk":"@@ ...","created_at":"2026-04-10T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_review_comments"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(42),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Review Comments (1)")
	assert.Contains(t, text, "### src/foo.go")
	assert.Contains(t, text, "**Line 42** — @alice")
	assert.Contains(t, text, "nil-check")
}

func TestListPRReviewComments_MissingNumber(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_review_comments"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestDiffPR_FormatsWithSummary(t *testing.T) {
	diffText := "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,4 @@\n line1\n+added\n line2\n line3"
	h := NewHandler(&mockGHClient{
		diffPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return diffText, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_diff_pr"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(1),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Files changed (1)")
	assert.Contains(t, text, "foo.go")
	assert.Contains(t, text, "+1 -0")
	assert.Contains(t, text, "## Diff")
	assert.Contains(t, text, "+added")
}

func TestListPRs_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		listPRsFunc: func(_ context.Context, owner, repo string, opts gh.ListPROpts) (string, error) {
			return `[{"number":1,"title":"Fix bug","state":"OPEN","author":{"login":"alice"},"headRefName":"fix-bug","isDraft":false,"updatedAt":"2025-01-02T00:00:00Z"},{"number":2,"title":"Add feature","state":"OPEN","author":{"login":"bob"},"headRefName":"add-feat","isDraft":true,"updatedAt":"2025-01-03T00:00:00Z"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_prs"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "**#1** Fix bug")
	assert.Contains(t, text, "@alice")
	assert.Contains(t, text, "**#2** Add feature")
	assert.Contains(t, text, "DRAFT")
}

func TestPRToolAnnotations(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	tools := h.prTools()
	byName := make(map[string]gomcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	cases := []struct {
		name     string
		expected gomcp.ToolAnnotation
	}{
		{"gh_create_pr", annAdditive},
		{"gh_view_pr", annRead},
		{"gh_list_prs", annRead},
		{"gh_diff_pr", annRead},
		{"gh_comment_pr", annAdditive},
		{"gh_review_pr", annAdditive},
		{"gh_merge_pr", annDestructive},
		{"gh_edit_pr", annIdempotent},
		{"gh_list_pr_checks", annRead},
		{"gh_close_pr", annDestructive},
		{"gh_list_pr_comments", annRead},
		{"gh_list_pr_reviews", annRead},
		{"gh_list_pr_review_comments", annRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := byName[tc.name]
			require.True(t, ok, "tool %s not registered", tc.name)
			assert.Equal(t, tc.expected, tool.Annotations)
		})
	}
}

func TestCheckPR_FormatsMarkdown(t *testing.T) {
	h := NewHandler(&mockGHClient{
		checkPRFunc: func(_ context.Context, owner, repo string, number int) (string, error) {
			return `[{"name":"build","state":"SUCCESS","link":""},{"name":"test","state":"FAILURE","link":"https://example.com/run/1"}]`, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_list_pr_checks"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(1),
	}
	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	text := result.Content[0].(gomcp.TextContent).Text
	assert.Contains(t, text, "## Status Checks (2)")
	assert.Contains(t, text, "- build: SUCCESS")
	assert.Contains(t, text, "- test: FAILURE (https://example.com/run/1)")
}

func TestReviewPR_EventEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_review_pr" {
			continue
		}
		prop := tool.InputSchema.Properties["event"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "event must declare an enum")
		assert.ElementsMatch(t, []string{"approve", "request_changes", "comment"}, enum)
		return
	}
	t.Fatal("gh_review_pr not found")
}

func TestMergePR_MethodEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_merge_pr" {
			continue
		}
		prop := tool.InputSchema.Properties["method"].(map[string]any)
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "method must declare an enum")
		assert.ElementsMatch(t, []string{"merge", "squash", "rebase"}, enum)
		return
	}
	t.Fatal("gh_merge_pr not found")
}

func TestViewPR_ParseError_TerseMessage(t *testing.T) {
	mock := &mockGHClient{
		viewPRFunc: func(ctx context.Context, owner, repo string, number int) (string, error) {
			return "this is not valid JSON", nil
		},
	}
	h := NewHandler(mock)

	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_view_pr"
	req.Params.Arguments = map[string]any{
		"owner":     "octocat",
		"repo":      "hello-world",
		"pr_number": float64(1),
	}

	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)

	require.Len(t, result.Content, 1)
	textContent, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "internal error: unable to parse gh output; check server logs", textContent.Text)
}

func TestCreatePR_BatchMissingFields(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "gh_create_pr"
	req.Params.Arguments = map[string]any{
		"owner": "octocat",
		"repo":  "hello-world",
		// title and body intentionally missing
	}

	result, err := h.Handle(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.IsError)

	require.Len(t, result.Content, 1)
	textContent, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	// Must mention BOTH missing fields.
	assert.Contains(t, textContent.Text, "title")
	assert.Contains(t, textContent.Text, "body")
}

func TestListPRs_StateEnum(t *testing.T) {
	h := NewHandler(&mockGHClient{})
	for _, tool := range h.prTools() {
		if tool.Name != "gh_list_prs" {
			continue
		}
		prop, ok := tool.InputSchema.Properties["state"].(map[string]any)
		require.True(t, ok, "state property missing or wrong shape")
		enum, ok := prop["enum"].([]string)
		require.True(t, ok, "state must declare an enum")
		assert.ElementsMatch(t, []string{"open", "closed", "merged", "all"}, enum)
		return
	}
	t.Fatal("gh_list_prs not found")
}
