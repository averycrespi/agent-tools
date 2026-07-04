package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runDirFunc func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return m.RunDir(ctx, "", name, args...)
}

func (m *mockRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if m.runDirFunc != nil {
		return m.runDirFunc(ctx, dir, name, args...)
	}
	return nil, nil
}

func mustNewClient(t *testing.T, runner *mockRunner) *Client {
	t.Helper()
	client, err := NewClient(runner, nil, true)
	require.NoError(t, err)
	return client
}

type gitCall struct {
	dir  string
	name string
	args []string
}

func TestRemoteOperations_RejectURLShapedRemoteBeforeRunningGit(t *testing.T) {
	calledGit := false
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	})

	_, err := c.Fetch(context.Background(), "/repo", "https://example.com/repo.git", "")

	require.Error(t, err)
	assert.ErrorContains(t, err, "remote must be a configured remote name")
	assert.False(t, calledGit)
}

func TestRemoteOperations_RejectUnknownRemoteBeforeFetch(t *testing.T) {
	var calls []gitCall
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calls = append(calls, gitCall{dir: dir, name: name, args: append([]string(nil), args...)})
			if assert.Equal(t, []string{"remote", "get-url", "--", "missing"}, args) {
				return []byte("error: No such remote 'missing'"), fmt.Errorf("exit status 2")
			}
			return nil, nil
		},
	})

	_, err := c.Fetch(context.Background(), "/repo", "missing", "")

	require.Error(t, err)
	assert.ErrorContains(t, err, "remote \"missing\" is not configured")
	require.Len(t, calls, 1)
}

func TestRemoteOperations_ValidateConfiguredRemoteBeforeRunningGit(t *testing.T) {
	var calls []gitCall
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calls = append(calls, gitCall{dir: dir, name: name, args: append([]string(nil), args...)})
			return []byte("ok\n"), nil
		},
	})

	_, err := c.Fetch(context.Background(), "/repo", "origin", "refs/heads/main")

	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Equal(t, gitCall{dir: "/repo", name: "git", args: []string{"remote", "get-url", "--", "origin"}}, calls[0])
	assert.Equal(t, gitCall{dir: "/repo", name: "git", args: []string{"fetch", "--", "origin", "refs/heads/main"}}, calls[1])
}

func TestRemoteOperations_RejectURLShapedRefspec(t *testing.T) {
	calledGit := false
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	})

	_, err := c.Push(context.Background(), "/repo", "origin", "https://example.com/repo.git", false)

	require.Error(t, err)
	assert.ErrorContains(t, err, "refspec must not be a URL")
	assert.False(t, calledGit)
}

func TestPush_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Everything up-to-date\n"), nil
		},
	})
	out, err := c.Push(context.Background(), "/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Everything up-to-date", out)
	assert.Equal(t, []string{"push", "--", "origin"}, capturedArgs)
}

func TestPush_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestPush_ForceWithLease(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "--", "origin"}, capturedArgs)
}

func TestPush_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "remote" && args[1] == "get-url" {
				return []byte("git@github.com:user/repo.git\n"), nil
			}
			return []byte("error: failed to push"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.Push(context.Background(), "/repo", "origin", "", false)
	assert.ErrorContains(t, err, "git push failed")
}

func TestPull_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Already up to date.\n"), nil
		},
	})
	out, err := c.Pull(context.Background(), "/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Already up to date.", out)
	assert.Equal(t, []string{"pull", "--", "origin"}, capturedArgs)
}

func TestPull_WithBranch(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull(context.Background(), "/repo", "origin", "main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--", "origin", "main"}, capturedArgs)
}

func TestPull_WithRebase(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull(context.Background(), "/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--rebase", "--", "origin"}, capturedArgs)
}

func TestFetch_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch(context.Background(), "/repo", "origin", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin"}, capturedArgs)
}

func TestFetch_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch(context.Background(), "/repo", "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestFetch_TimesOutBlockedCommand(t *testing.T) {
	c, err := NewClientWithTimeout(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "remote" && args[1] == "get-url" {
				return []byte("git@github.com:user/repo.git\n"), nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, nil, true, time.Millisecond)
	require.NoError(t, err)

	_, err = c.Fetch(context.Background(), "/repo", "origin", "")

	assert.ErrorContains(t, err, "git fetch failed")
	assert.ErrorContains(t, err, context.DeadlineExceeded.Error())
}

func TestListRemoteRefs_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("abc123\trefs/heads/main\ndef456\trefs/heads/feature\n"), nil
		},
	})
	refs, err := c.ListRemoteRefs(context.Background(), "/repo", "origin")
	require.NoError(t, err)
	assert.Equal(t, []Ref{
		{SHA: "abc123", Ref: "refs/heads/main"},
		{SHA: "def456", Ref: "refs/heads/feature"},
	}, refs)
}

func TestListRemoteRefs_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "remote" && args[1] == "get-url" {
				return []byte("git@github.com:user/repo.git\n"), nil
			}
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemoteRefs(context.Background(), "/repo", "origin")
	assert.ErrorContains(t, err, "git ls-remote failed")
}

func TestListRemotes_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("origin\tgit@github.com:user/repo.git (fetch)\norigin\tgit@github.com:user/repo.git (push)\n"), nil
		},
	})
	remotes, err := c.ListRemotes(context.Background(), "/repo")
	require.NoError(t, err)
	assert.Equal(t, []Remote{
		{Name: "origin", FetchURL: "git@github.com:user/repo.git", PushURL: "git@github.com:user/repo.git"},
	}, remotes)
}

func TestListRemotes_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemotes(context.Background(), "/repo")
	assert.ErrorContains(t, err, "git remote failed")
}

func TestValidateRepo_RelativePath(t *testing.T) {
	c := mustNewClient(t, &mockRunner{})
	_, err := c.ValidateRepo(context.Background(), "relative/path")
	assert.ErrorContains(t, err, "must be an absolute path")
}

func TestValidateRepo_NotAGitRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ValidateRepo(context.Background(), repo)
	assert.ErrorContains(t, err, "not a git repository")
}

func TestValidateRepo_AllowedExactPath(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{repo}, false)
	require.NoError(t, err)
	validatedPath, err := c.ValidateRepo(context.Background(), repo)
	require.NoError(t, err)
	assert.Equal(t, repo, validatedPath)
	assert.Equal(t, repo, capturedDir)
}

func TestValidateRepo_ReturnsCleanedPath(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{repo}, false)
	require.NoError(t, err)
	validatedPath, err := c.ValidateRepo(context.Background(), filepath.Join(root, "other", "..", "repo"))
	require.NoError(t, err)
	assert.Equal(t, repo, validatedPath)
	assert.Equal(t, repo, capturedDir)
}

func TestValidateRepo_AllowedDescendantPath(t *testing.T) {
	allowed := t.TempDir()
	repo := filepath.Join(allowed, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, []string{allowed}, false)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), repo)
	require.NoError(t, err)
}

func TestValidateRepo_RejectsSymlinkEscapeFromAllowedPath(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	escape := filepath.Join(allowed, "escape")
	require.NoError(t, os.Symlink(outside, escape))

	calledGit := false
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return []byte(".git\n"), nil
		},
	}, []string{allowed}, false)
	require.NoError(t, err)

	_, err = c.ValidateRepo(context.Background(), escape)

	require.Error(t, err)
	assert.ErrorContains(t, err, "outside allowed paths")
	assert.False(t, calledGit)
}

func TestValidateRepo_ResolvesSymlinkedAllowedPath(t *testing.T) {
	root := t.TempDir()
	realAllowed := filepath.Join(root, "real-allowed")
	repo := filepath.Join(realAllowed, "repo")
	linkAllowed := filepath.Join(root, "allowed-link")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.Symlink(realAllowed, linkAllowed))

	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{linkAllowed}, false)
	require.NoError(t, err)

	validatedPath, err := c.ValidateRepo(context.Background(), filepath.Join(linkAllowed, "repo"))

	require.NoError(t, err)
	assert.Equal(t, repo, validatedPath)
	assert.Equal(t, repo, capturedDir)
}

func TestValidateRepo_RejectsSiblingPrefix(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "repo")
	sibling := filepath.Join(root, "repo2")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	calledGit := false
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	}, []string{allowed}, false)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), sibling)
	assert.ErrorContains(t, err, "outside allowed paths")
	assert.ErrorContains(t, err, allowed)
	assert.False(t, calledGit)
}

func TestValidateRepo_Valid(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	})
	_, err := c.ValidateRepo(context.Background(), repo)
	require.NoError(t, err)
}

func TestNewClient_RequiresAllowedPaths(t *testing.T) {
	_, err := NewClient(&mockRunner{}, nil, false)
	assert.ErrorContains(t, err, "at least one allowed path is required")
}

func TestNewClient_RejectsRelativeAllowedPath(t *testing.T) {
	_, err := NewClient(&mockRunner{}, []string{"relative/path"}, false)
	assert.ErrorContains(t, err, "allowed path must be absolute")
}

func TestNewClient_RejectsAllowAllWithExplicitPaths(t *testing.T) {
	_, err := NewClient(&mockRunner{}, []string{"/repo"}, true)
	assert.ErrorContains(t, err, "cannot be combined")
}

func TestNewClient_RejectsMissingAllowedPath(t *testing.T) {
	_, err := NewClient(&mockRunner{}, []string{filepath.Join(t.TempDir(), "missing")}, false)
	assert.ErrorContains(t, err, "allowed path must exist")
}

func TestNewClient_AllowAllPaths(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	c, err := NewClient(&mockRunner{
		runDirFunc: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, nil, true)
	require.NoError(t, err)
	_, err = c.ValidateRepo(context.Background(), repo)
	require.NoError(t, err)
}
