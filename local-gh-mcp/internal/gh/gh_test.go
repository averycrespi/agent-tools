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
	assert.Contains(t, args, "31")
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

func TestViewIssue_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ViewIssue(context.Background(), "octocat", "hello", 99)
	require.NoError(t, err)
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "99")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, issueViewFields)
}

func TestListIssues_WithFilters(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListIssues(context.Background(), "octocat", "hello", ListIssuesOpts{
		State: "open",
		Label: "bug",
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--state")
	assert.Contains(t, args, "open")
	assert.Contains(t, args, "--label")
	assert.Contains(t, args, "bug")
	assert.Contains(t, args, "--limit")
	assert.Contains(t, args, "11")
}

func TestCommentIssue_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CommentIssue(context.Background(), "octocat", "hello", 5, "Thanks for reporting!")
	require.NoError(t, err)
	assert.Contains(t, args, "--body")
	assert.Contains(t, args, "Thanks for reporting!")
	assert.Contains(t, args, "5")
}

func TestListRuns_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListRuns(context.Background(), "octocat", "hello", ListRunsOpts{
		Branch: "main",
		Status: "failure",
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--branch")
	assert.Contains(t, args, "main")
	assert.Contains(t, args, "--status")
	assert.Contains(t, args, "failure")
}

func TestViewRun_WithFailedLogs(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ViewRun(context.Background(), "octocat", "hello", "12345", true)
	require.NoError(t, err)
	assert.Contains(t, args, "--log-failed")
	assert.NotContains(t, args, "--json")
	assert.Contains(t, args, "12345")
}

func TestViewRun_WithoutLogs(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ViewRun(context.Background(), "octocat", "hello", "12345", false)
	require.NoError(t, err)
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, runViewFields)
	assert.NotContains(t, args, "--log-failed")
	assert.Contains(t, args, "12345")
}

func TestRerun_FailedOnly(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.Rerun(context.Background(), "octocat", "hello", "12345", true)
	require.NoError(t, err)
	assert.Contains(t, args, "--failed")
	assert.Contains(t, args, "12345")
}

func TestCancelRun_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CancelRun(context.Background(), "octocat", "hello", "67890")
	require.NoError(t, err)
	assert.Contains(t, args, "67890")
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
}

func TestListCaches_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListCaches(context.Background(), "octocat", "hello", ListCachesOpts{Limit: 20})
	require.NoError(t, err)
	assert.Contains(t, args, "--limit")
	assert.Contains(t, args, "21")
}

func TestDeleteCache_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.DeleteCache(context.Background(), "octocat", "hello", "cache-abc-123")
	require.NoError(t, err)
	assert.Contains(t, args, "cache-abc-123")
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
}

func TestSearchPRs_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), "fixme", SearchPRsOpts{
		Repo:  "octocat/hello",
		State: "open",
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--repo")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "--")
	postDash := postDashTokens(t, args)
	assert.Equal(t, []string{"fixme", "is:open"}, postDash, "query and is:<state> token must follow --")
}

// TestSearchPRs_JSONFields verifies that the projection requests body and
// updatedAt and drops url/createdAt — search excerpts depend on body, and
// url/createdAt are not rendered.
func TestSearchPRs_JSONFields(t *testing.T) {
	assert.Equal(t, "number,title,state,author,repository,body,updatedAt", searchPRFields)
}

// TestSearchIssues_JSONFields verifies the issue-search projection mirrors
// SearchPRs: body in, url/createdAt out.
func TestSearchIssues_JSONFields(t *testing.T) {
	assert.Equal(t, "number,title,state,author,repository,body,updatedAt", searchIssueFields)
}

// TestSearchPRs_DSLTokenization is the regression test for the search query
// mangling bug: a multi-token DSL query must be split into separate argv
// positionals so `gh search` doesn't quote it as a single phrase.
func TestSearchPRs_DSLTokenization(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), "is:open repo:foo/bar label:bug", SearchPRsOpts{})
	require.NoError(t, err)
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dashIdx, "expected -- separator")
	assert.Equal(t, []string{"is:open", "repo:foo/bar", "label:bug"}, args[dashIdx+1:])
}

// TestSearchPRs_QuotedPhrase verifies shlex preserves quoted phrases as a
// single token (e.g. for `"hello world" in:title` queries).
func TestSearchPRs_QuotedPhrase(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), `"hello world" in:title`, SearchPRsOpts{})
	require.NoError(t, err)
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dashIdx, "expected -- separator")
	assert.Equal(t, []string{"hello world", "in:title"}, args[dashIdx+1:])
}

// TestSearchPRs_InvalidQuery verifies an unbalanced-quote query surfaces a
// clean error rather than panicking or invoking gh.
func TestSearchPRs_InvalidQuery(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(_ string, _ ...string) ([]byte, error) {
			t.Fatal("runner must not be invoked on invalid query")
			return nil, nil
		},
	})
	_, err := c.SearchPRs(context.Background(), `is:open "unterminated`, SearchPRsOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid search query")
}

// TestSearchPRs_ShellMetacharsAreInert is a defense-in-depth guard: shell
// metacharacters in the query must arrive at the runner as literal argv
// tokens, never as a single shell-interpreted string. Runner.Run uses
// os/exec.Command (no shell), so this is a regression guard against future
// changes that might introduce shell invocation.
func TestSearchPRs_ShellMetacharsAreInert(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), "; rm -rf / && cat /etc/passwd", SearchPRsOpts{})
	require.NoError(t, err)
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dashIdx, "expected -- separator")
	// All metacharacter-bearing tokens must arrive as separate, unmodified argv
	// elements — never collapsed into a shell-interpretable string.
	assert.Equal(t, []string{";", "rm", "-rf", "/", "&&", "cat", "/etc/passwd"}, args[dashIdx+1:])
}

// TestSearchPRs_FlagInjectionAfterSeparator is a regression guard for the
// `--` separator: even if a query token looks like a gh flag (e.g.
// `--repo=evil/repo`), it must land after the `--` so gh treats it as a
// positional, not a flag override.
func TestSearchPRs_FlagInjectionAfterSeparator(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), "--repo=attacker/repo --token=stolen", SearchPRsOpts{
		Repo: "legit/repo",
	})
	require.NoError(t, err)
	// Find the -- separator index; everything after it is positional query tokens.
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dashIdx, "expected -- separator")
	// The legit Repo flag must appear *before* the separator.
	preDash := args[:dashIdx]
	assert.Contains(t, preDash, "--repo")
	assert.Contains(t, preDash, "legit/repo")
	// The flag-shaped query tokens must appear *after* the separator, where
	// gh treats them as positional query text rather than CLI flags.
	postDash := args[dashIdx+1:]
	assert.Equal(t, []string{"--repo=attacker/repo", "--token=stolen"}, postDash)
}

// TestSearchPRs_SubshellSyntaxIsLiteral verifies that subshell-style patterns
// like $(...) and backticks survive as literal argv content. shlex without
// POSIX mode does not perform command substitution; this test freezes that
// guarantee.
func TestSearchPRs_SubshellSyntaxIsLiteral(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchPRs(context.Background(), "$(echo pwned) `whoami`", SearchPRsOpts{})
	require.NoError(t, err)
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	require.NotEqual(t, -1, dashIdx, "expected -- separator")
	postDash := args[dashIdx+1:]
	// shlex.Split tokenizes on whitespace; the literal characters $, (, ), `
	// must survive verbatim. No subprocess of any kind has run on this string.
	assert.Equal(t, []string{"$(echo", "pwned)", "`whoami`"}, postDash)
}

// postDashTokens returns the argv slice after the `--` separator. Used by the
// search-state tests to verify which qualifiers landed in the query payload
// versus which became gh CLI flags.
func postDashTokens(t *testing.T, args []string) []string {
	t.Helper()
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	t.Fatal("expected -- separator in argv")
	return nil
}

// All four state cases for SearchPRs follow the same shape: the wrapper must
// NOT pass `--state` (gh search prs --state is unreliable when combined with
// --repo) and must inject the equivalent `is:<state>` token into the query.
func TestSearchPRs_StateAsQueryQualifier(t *testing.T) {
	cases := []struct {
		state    string
		wantPost string // expected token in post-`--` argv ("" means none)
	}{
		{"open", "is:open"},
		{"closed", "is:closed"},
		{"merged", "is:merged"},
		{"all", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			var args []string
			c := NewClient(capturedArgs(t, &args))
			_, err := c.SearchPRs(context.Background(), "fixme", SearchPRsOpts{State: tc.state})
			require.NoError(t, err)
			assert.NotContains(t, args, "--state", "--state must never appear; inject is:<state> as a query token instead")
			postDash := postDashTokens(t, args)
			if tc.wantPost != "" {
				assert.Contains(t, postDash, tc.wantPost)
			} else {
				for _, q := range []string{"is:open", "is:closed", "is:merged"} {
					assert.NotContains(t, postDash, q)
				}
			}
		})
	}
}

// SearchIssues mirrors SearchPRs: state must be injected as `is:<state>`
// rather than passed via --state, since `gh search issues --state` interacts
// badly with --repo (mirrors the prs-search bug).
func TestSearchIssues_StateAsQueryQualifier(t *testing.T) {
	cases := []struct {
		state    string
		wantPost string
	}{
		{"open", "is:open"},
		{"closed", "is:closed"},
		{"all", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			var args []string
			c := NewClient(capturedArgs(t, &args))
			_, err := c.SearchIssues(context.Background(), "memory leak", SearchIssuesOpts{State: tc.state})
			require.NoError(t, err)
			assert.NotContains(t, args, "--state")
			postDash := postDashTokens(t, args)
			if tc.wantPost != "" {
				assert.Contains(t, postDash, tc.wantPost)
			} else {
				for _, q := range []string{"is:open", "is:closed"} {
					assert.NotContains(t, postDash, q)
				}
			}
		})
	}
}

func TestSearchCode_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.SearchCode(context.Background(), "fmt.Errorf", SearchCodeOpts{
		Language:  "go",
		Extension: "go",
	})
	require.NoError(t, err)
	assert.Contains(t, args, "--language")
	assert.Contains(t, args, "go")
	assert.Contains(t, args, "--extension")
	assert.Contains(t, args, "go")
	assert.Contains(t, args, "--")
	assert.Equal(t, "fmt.Errorf", args[len(args)-1], "query token must come after --")
}

func TestViewRun_SeparatorBeforeRunID(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ViewRun(context.Background(), "octocat", "hello", "12345", false)
	require.NoError(t, err)
	assert.Contains(t, args, "--")
	assert.Equal(t, "12345", args[len(args)-1], "runID must be last arg after --")
}

func TestRerun_SeparatorBeforeRunID(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.Rerun(context.Background(), "octocat", "hello", "12345", false)
	require.NoError(t, err)
	assert.Contains(t, args, "--")
	assert.Equal(t, "12345", args[len(args)-1], "runID must be last arg after --")
}

func TestCancelRun_SeparatorBeforeRunID(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.CancelRun(context.Background(), "octocat", "hello", "67890")
	require.NoError(t, err)
	assert.Contains(t, args, "--")
	assert.Equal(t, "67890", args[len(args)-1], "runID must be last arg after --")
}

func TestPRComments_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.PRComments(context.Background(), "octocat", "hello", 42, 10)
	require.NoError(t, err)
	assert.Contains(t, args, "pr")
	assert.Contains(t, args, "view")
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, "comments")
	assert.Contains(t, args, "--jq")
	assert.Contains(t, args, "42")
}

func TestPRComments_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("not found"), fmt.Errorf("exit 1")
		},
	})
	_, err := c.PRComments(context.Background(), "o", "r", 1, 10)
	assert.ErrorContains(t, err, "gh pr comments failed")
}

func TestIssueComments_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.IssueComments(context.Background(), "octocat", "hello", 99, 5)
	require.NoError(t, err)
	assert.Contains(t, args, "issue")
	assert.Contains(t, args, "view")
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, "comments")
	assert.Contains(t, args, "--jq")
	assert.Contains(t, args, "99")
}

func TestIssueComments_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("not found"), fmt.Errorf("exit 1")
		},
	})
	_, err := c.IssueComments(context.Background(), "o", "r", 1, 10)
	assert.ErrorContains(t, err, "gh issue comments failed")
}

func TestPRReviews_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.PRReviews(context.Background(), "octocat", "hello", 42, 10)
	require.NoError(t, err)
	assert.Contains(t, args, "pr")
	assert.Contains(t, args, "view")
	assert.Contains(t, args, "-R")
	assert.Contains(t, args, "octocat/hello")
	assert.Contains(t, args, "--json")
	assert.Contains(t, args, "reviews")
	assert.Contains(t, args, "--jq")
	assert.Contains(t, args, "42")
}

func TestPRReviews_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("not found"), fmt.Errorf("exit 1")
		},
	})
	_, err := c.PRReviews(context.Background(), "o", "r", 1, 10)
	assert.ErrorContains(t, err, "gh pr reviews failed")
}

func TestPRReviewComments_Args(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.PRReviewComments(context.Background(), "octocat", "hello", 42, 10)
	require.NoError(t, err)
	assert.Contains(t, args, "api")
	assert.Contains(t, args, "--jq")
	assert.Contains(t, args, "--")
	endpoint := args[len(args)-1]
	assert.Equal(t, "repos/octocat/hello/pulls/42/comments?per_page=11", endpoint)
}

func TestPRReviewComments_JqIncludesAuthorAssociation(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.PRReviewComments(context.Background(), "octocat", "hello", 42, 10)
	require.NoError(t, err)
	// Find the --jq value (one arg after `--jq`).
	var jq string
	for i, a := range args {
		if a == "--jq" && i+1 < len(args) {
			jq = args[i+1]
			break
		}
	}
	assert.Contains(t, jq, "author_association", "jq projection should request author_association")
}

func TestPRReviewComments_ClampsLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.PRReviewComments(context.Background(), "octocat", "hello", 42, 500)
	require.NoError(t, err)
	endpoint := args[len(args)-1]
	assert.Contains(t, endpoint, "per_page=100", "limit above maxLimit should clamp to 100")
}

func TestPRReviewComments_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(name string, args ...string) ([]byte, error) {
			return []byte("not found"), fmt.Errorf("exit 1")
		},
	})
	_, err := c.PRReviewComments(context.Background(), "o", "r", 1, 10)
	assert.ErrorContains(t, err, "gh pr review comments failed")
}

func TestDeleteCache_SeparatorBeforeCacheID(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.DeleteCache(context.Background(), "octocat", "hello", "cache-abc-123")
	require.NoError(t, err)
	assert.Contains(t, args, "--")
	assert.Equal(t, "cache-abc-123", args[len(args)-1], "cacheID must be last arg after --")
}

func TestListBranches_PassesPageAndPerPage(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListBranches(context.Background(), "octocat", "hello", 30, 2)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(args), 2)
	assert.Equal(t, "api", args[0])
	assert.Contains(t, args[1], "per_page=31")
	assert.Contains(t, args[1], "page=2")
}

func TestListBranches_PageDefaultsAtLeastOne(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListBranches(context.Background(), "octocat", "hello", 30, 0)
	require.NoError(t, err)
	assert.Contains(t, args[1], "page=1")
}

func TestListBranches_ClampsLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListBranches(context.Background(), "octocat", "hello", 999, 1)
	require.NoError(t, err)
	// limit clamped to 100, perPage = limit+1 then capped at 100
	assert.Contains(t, args[1], "per_page=100")
}

func TestListReleases_ClampsLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListReleases(context.Background(), "octocat", "hello", 999)
	require.NoError(t, err)
	// 999 clamps to 100, +1 for truncation peek capped back at 100
	assert.Contains(t, args, "100")
}

func TestListReleases_OmitsAuthorField(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListReleases(context.Background(), "octocat", "hello", 30)
	require.NoError(t, err)
	var jsonFields string
	for i, a := range args {
		if a == "--json" && i+1 < len(args) {
			jsonFields = args[i+1]
			break
		}
	}
	assert.NotContains(t, jsonFields, "author", "`gh release list --json` rejects the author field; only `gh release view --json` supports it")
}

func TestListPRFiles_ClampsLimit(t *testing.T) {
	var args []string
	c := NewClient(capturedArgs(t, &args))
	_, err := c.ListPRFiles(context.Background(), "octocat", "hello", 1, 999)
	require.NoError(t, err)
	// 999 clamps to 100, perPage = 101 capped at 100
	assert.Contains(t, args[1], "per_page=100")
}

func TestCleanAPIError_PrefersGhStatusLine(t *testing.T) {
	out := []byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/..."}` + "\n" + `gh: Not Found (HTTP 404)`)
	got := cleanAPIError(out)
	assert.Equal(t, "gh: Not Found (HTTP 404)", got)
}

func TestCleanAPIError_FallsBackWhenNoGhLine(t *testing.T) {
	out := []byte("something unexpected")
	got := cleanAPIError(out)
	assert.Equal(t, "something unexpected", got)
}

func TestViewRunJobLog_StripsUnknownStepAndBOM(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(_ string, _ ...string) ([]byte, error) {
			// First line carries a UTF-8 BOM and the UNKNOWN STEP placeholder.
			body := "\uFEFFbuild\tUNKNOWN STEP\t2025-04-26T00:00:00Z hello\n" +
				"build\tcompile\t2025-04-26T00:00:01Z world\n"
			return []byte(body), nil
		},
	})
	out, err := c.ViewRunJobLog(context.Background(), "o", "r", 42, 0)
	require.NoError(t, err)
	assert.NotContains(t, out, "UNKNOWN STEP")
	assert.NotContains(t, out, "\uFEFF")
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "world")
}

func TestListBranches_ErrorIsClean(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(_ string, _ ...string) ([]byte, error) {
			body := `{"message":"Not Found","documentation_url":"https://x"}`
			return []byte(body + "\ngh: Not Found (HTTP 404)"), assert.AnError
		},
	})
	_, err := c.ListBranches(context.Background(), "o", "r", 30, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gh: Not Found (HTTP 404)")
	assert.NotContains(t, err.Error(), "documentation_url")
}

// TestCleanAPIError_ConcatenatedJSONAndGhLine covers the case where gh
// appends the error line directly after the JSON body without a newline.
func TestCleanAPIError_ConcatenatedJSONAndGhLine(t *testing.T) {
	out := []byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/..."}gh: Not Found (HTTP 404)`)
	got := cleanAPIError(out)
	assert.Equal(t, "gh: Not Found (HTTP 404)", got)
}

// TestCleanGhError_PrefersErrorLine covers the normal search failure path
// where gh prints an Error: line followed by usage/banner text.
func TestCleanGhError_PrefersErrorLine(t *testing.T) {
	out := []byte("Error: invalid value \"bogus\" for flag --state\n\nUsage:\n  gh search prs [<query>] [flags]\n\nFlags:\n  -h, --help   help for prs\n")
	got := cleanGhError(out)
	assert.Equal(t, `Error: invalid value "bogus" for flag --state`, got)
}

// TestCleanGhError_FallsBackToFirstNonEmptyLine covers output with no Error:
// line — the first non-empty line is returned.
func TestCleanGhError_FallsBackToFirstNonEmptyLine(t *testing.T) {
	out := []byte("\n\nsome other message\nmore text\n")
	got := cleanGhError(out)
	assert.Equal(t, "some other message", got)
}

// TestCleanGhError_FallsBackToTrimmedInput covers blank output.
func TestCleanGhError_FallsBackToTrimmedInput(t *testing.T) {
	out := []byte("  \n  ")
	got := cleanGhError(out)
	assert.Equal(t, "", got)
}

// TestNormalizeRunLog covers the four combinations of BOM and UNKNOWN STEP.
func TestNormalizeRunLog_BOMOnly(t *testing.T) {
	in := "\uFEFFbuild\tcompile\t2025-01-01T00:00:00Z message\n"
	got := normalizeRunLog(in)
	assert.NotContains(t, got, "\uFEFF")
	assert.Contains(t, got, "build\tcompile")
}

func TestNormalizeRunLog_UnknownStepOnly(t *testing.T) {
	in := "build\tUNKNOWN STEP\t2025-01-01T00:00:00Z message\n"
	got := normalizeRunLog(in)
	assert.NotContains(t, got, "UNKNOWN STEP")
	assert.Contains(t, got, "build\t2025-01-01")
}

func TestNormalizeRunLog_BOMAndUnknownStep(t *testing.T) {
	in := "\uFEFFbuild\tUNKNOWN STEP\t2025-01-01T00:00:00Z message\n"
	got := normalizeRunLog(in)
	assert.NotContains(t, got, "\uFEFF")
	assert.NotContains(t, got, "UNKNOWN STEP")
	assert.Contains(t, got, "build\t2025-01-01")
}

func TestNormalizeRunLog_NeitherBOMNorUnknownStep(t *testing.T) {
	in := "build\tcompile\t2025-01-01T00:00:00Z message\n"
	got := normalizeRunLog(in)
	assert.Equal(t, "build\tcompile\t2025-01-01T00:00:00Z message", got)
}

// TestNormalizeRunLog_BOMOnlyLeading verifies that a BOM appearing mid-string
// is NOT stripped — only a leading BOM is removed.
func TestNormalizeRunLog_BOMOnlyLeading(t *testing.T) {
	in := "line1\n\uFEFFline2\n"
	got := normalizeRunLog(in)
	assert.Contains(t, got, "\uFEFF", "mid-string BOM must be preserved")
}

// TestViewRun_LogFailed_NormalizesOutput verifies that the logFailed=true path
// strips the BOM and UNKNOWN STEP noise before returning.
func TestViewRun_LogFailed_NormalizesOutput(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(_ string, _ ...string) ([]byte, error) {
			body := "\uFEFFbuild\tUNKNOWN STEP\t2025-01-01T00:00:00Z failed here\n" +
				"build\tsetup\t2025-01-01T00:00:01Z all good\n"
			return []byte(body), nil
		},
	})
	out, err := c.ViewRun(context.Background(), "o", "r", "99", true)
	require.NoError(t, err)
	assert.NotContains(t, out, "\uFEFF")
	assert.NotContains(t, out, "UNKNOWN STEP")
	assert.Contains(t, out, "failed here")
	assert.Contains(t, out, "all good")
}

// TestViewRunJobLog_NormalizesOutput is the parallel test for ViewRunJobLog,
// confirming it still strips BOM and UNKNOWN STEP after the refactor.
func TestViewRunJobLog_NormalizesOutput(t *testing.T) {
	c := NewClient(&mockRunner{
		runFunc: func(_ string, _ ...string) ([]byte, error) {
			body := "\uFEFFbuild\tUNKNOWN STEP\t2025-01-01T00:00:00Z step one\n" +
				"build\trun\t2025-01-01T00:00:01Z step two\n"
			return []byte(body), nil
		},
	})
	out, err := c.ViewRunJobLog(context.Background(), "o", "r", 7, 0)
	require.NoError(t, err)
	assert.NotContains(t, out, "\uFEFF")
	assert.NotContains(t, out, "UNKNOWN STEP")
	assert.Contains(t, out, "step one")
	assert.Contains(t, out, "step two")
}
