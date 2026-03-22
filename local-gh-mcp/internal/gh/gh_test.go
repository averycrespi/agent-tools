package gh

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedArgs is a helper to capture the args passed to the mock runner.
func capturedArgs(t *testing.T, captured *[]string) *mockRunner {
	return &mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			*captured = args
			return []byte("ok"), nil
		},
	}
}

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runFunc func(name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	if m.runFunc != nil {
		return m.runFunc(name, args...)
	}
	return nil, nil
}

func TestAuthStatus_Success(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			assert.Equal(t, []string{"auth", "status"}, args)
			return []byte("Logged in to github.com"), nil
		},
	})
	err := c.AuthStatus(context.Background())
	require.NoError(t, err)
}

func TestAuthStatus_Failure(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("You are not logged into any GitHub hosts"), fmt.Errorf("exit status 1")
		},
	})
	err := c.AuthStatus(context.Background())
	assert.ErrorContains(t, err, "gh auth status failed")
}

func TestValidateOwnerRepo_Valid(t *testing.T) {
	assert.NoError(t, ValidateOwnerRepo("octocat", "hello-world"))
	assert.NoError(t, ValidateOwnerRepo("my.org", "repo_name"))
	assert.NoError(t, ValidateOwnerRepo("user-123", "repo.v2"))
}

func TestValidateOwnerRepo_Invalid(t *testing.T) {
	assert.Error(t, ValidateOwnerRepo("", "repo"))
	assert.Error(t, ValidateOwnerRepo("owner", ""))
	assert.Error(t, ValidateOwnerRepo("owner/evil", "repo"))
	assert.Error(t, ValidateOwnerRepo("owner", "repo;rm -rf"))
	assert.Error(t, ValidateOwnerRepo("owner", "repo name"))
}

func TestCreatePR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CreatePR(context.Background(), "octocat", "hello", CreatePROpts{
		Title: "Fix bug",
		Body:  "Fixes #1",
		Draft: true,
	})
	require.NoError(t, err)
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "--title")
	assert.Contains(t, args, "Fix bug")
	assert.Contains(t, args, "--body")
	assert.Contains(t, args, "Fixes #1")
	assert.Contains(t, args, "--draft")
}

func TestCreatePR_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("validation failed"), fmt.Errorf("exit 1")
		},
	})
	_, err := c.CreatePR(context.Background(), "o", "r", CreatePROpts{Title: "t", Body: "b"})
	assert.ErrorContains(t, err, "gh pr create failed")
}

func TestViewPR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ViewPR(context.Background(), "octocat", "hello", 42)
	require.NoError(t, err)
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "42")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, prViewFields)
}

func TestListPRs_DefaultLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListPRs(context.Background(), "octocat", "hello", ListPROpts{})
	require.NoError(t, err)
	assert.Contains(t, args, "--limit")
	assert.Contains(t, args, "30")
}

func TestListPRs_ClampedLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListPRs(context.Background(), "octocat", "hello", ListPROpts{Limit: 500})
	require.NoError(t, err)
	assert.Contains(t, args, "--limit")
	assert.Contains(t, args, "100")
}

func TestDiffPR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.DiffPR(context.Background(), "octocat", "hello", 7)
	require.NoError(t, err)
	assert.Contains(t, args, "7")
	assert.NotContains(t, args, "--json")
}

func TestCommentPR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CommentPR(context.Background(), "octocat", "hello", 3, "LGTM")
	require.NoError(t, err)
	assert.Contains(t, args, "--body")
	assert.Contains(t, args, "LGTM")
}

func TestReviewPR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ReviewPR(context.Background(), "octocat", "hello", 5, "approve", "Looks good")
	require.NoError(t, err)
	assert.Contains(t, args, "--approve")
	assert.Contains(t, args, "--body")
	assert.Contains(t, args, "Looks good")
}

func TestMergePR_Squash(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.MergePR(context.Background(), "octocat", "hello", 10, MergePROpts{
		Method:       "squash",
		DeleteBranch: true,
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--squash")
	assert.Contains(t, args, "--delete-branch")
}

func TestEditPR_Labels(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.EditPR(context.Background(), "octocat", "hello", 8, EditPROpts{
		AddLabels:    []string{"bug", "urgent"},
		RemoveLabels: []string{"wontfix"},
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--add-label")
	assert.Contains(t, args, "bug,urgent")
	assert.Contains(t, args, "--remove-label")
	assert.Contains(t, args, "wontfix")
}

func TestCheckPR_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CheckPR(context.Background(), "octocat", "hello", 15)
	require.NoError(t, err)
	assert.Contains(t, args, "15")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, prCheckFields)
}

func TestClosePR_WithComment(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ClosePR(context.Background(), "octocat", "hello", 20, "Closing as duplicate")
	require.NoError(t, err)
	assert.Contains(t, args, "--comment")
	assert.Contains(t, args, "Closing as duplicate")
}
