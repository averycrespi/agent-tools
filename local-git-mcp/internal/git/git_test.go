package git

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a test double for exec.Runner.
type mockRunner struct {
	runDirFunc func(dir, name string, args ...string) ([]byte, error)
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	return m.RunDir("", name, args...)
}

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	if m.runDirFunc != nil {
		return m.runDirFunc(dir, name, args...)
	}
	return nil, nil
}

func mustNewClient(t *testing.T, runner *mockRunner) *Client {
	t.Helper()
	client, err := NewClient(runner, nil, true)
	require.NoError(t, err)
	return client
}

func TestPush_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Everything up-to-date\n"), nil
		},
	})
	out, err := c.Push("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Everything up-to-date", out)
	assert.Equal(t, []string{"push", "--", "origin"}, capturedArgs)
}

func TestPush_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestPush_ForceWithLease(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "--", "origin"}, capturedArgs)
}

func TestPush_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("error: failed to push"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.Push("/repo", "origin", "", false)
	assert.ErrorContains(t, err, "git push failed")
}

func TestPull_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Already up to date.\n"), nil
		},
	})
	out, err := c.Pull("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Already up to date.", out)
	assert.Equal(t, []string{"pull", "--", "origin"}, capturedArgs)
}

func TestPull_WithBranch(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--", "origin", "main"}, capturedArgs)
}

func TestPull_WithRebase(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--rebase", "--", "origin"}, capturedArgs)
}

func TestFetch_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin"}, capturedArgs)
}

func TestFetch_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "--", "origin", "refs/heads/main"}, capturedArgs)
}

func TestListRemoteRefs_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("abc123\trefs/heads/main\ndef456\trefs/heads/feature\n"), nil
		},
	})
	refs, err := c.ListRemoteRefs("/repo", "origin")
	require.NoError(t, err)
	assert.Equal(t, []Ref{
		{SHA: "abc123", Ref: "refs/heads/main"},
		{SHA: "def456", Ref: "refs/heads/feature"},
	}, refs)
}

func TestListRemoteRefs_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemoteRefs("/repo", "origin")
	assert.ErrorContains(t, err, "git ls-remote failed")
}

func TestListRemotes_Success(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("origin\tgit@github.com:user/repo.git (fetch)\norigin\tgit@github.com:user/repo.git (push)\n"), nil
		},
	})
	remotes, err := c.ListRemotes("/repo")
	require.NoError(t, err)
	assert.Equal(t, []Remote{
		{Name: "origin", FetchURL: "git@github.com:user/repo.git", PushURL: "git@github.com:user/repo.git"},
	}, remotes)
}

func TestListRemotes_Error(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	_, err := c.ListRemotes("/repo")
	assert.ErrorContains(t, err, "git remote failed")
}

func TestValidateRepo_RelativePath(t *testing.T) {
	c := mustNewClient(t, &mockRunner{})
	err := c.ValidateRepo("relative/path")
	assert.ErrorContains(t, err, "must be an absolute path")
}

func TestValidateRepo_NotAGitRepo(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	err := c.ValidateRepo("/some/path")
	assert.ErrorContains(t, err, "not a git repository")
}

func TestValidateRepo_AllowedExactPath(t *testing.T) {
	var capturedDir string
	c, err := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedDir = dir
			return []byte(".git\n"), nil
		},
	}, []string{"/some/repo"}, false)
	require.NoError(t, err)
	err = c.ValidateRepo("/some/repo")
	require.NoError(t, err)
	assert.Equal(t, "/some/repo", capturedDir)
}

func TestValidateRepo_AllowedDescendantPath(t *testing.T) {
	c, err := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, []string{"/some"}, false)
	require.NoError(t, err)
	err = c.ValidateRepo("/some/repo")
	require.NoError(t, err)
}

func TestValidateRepo_RejectsSiblingPrefix(t *testing.T) {
	calledGit := false
	c, err := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			calledGit = true
			return nil, nil
		},
	}, []string{"/repo"}, false)
	require.NoError(t, err)
	err = c.ValidateRepo("/repo2")
	assert.ErrorContains(t, err, "outside allowed paths")
	assert.ErrorContains(t, err, "/repo")
	assert.False(t, calledGit)
}

func TestValidateRepo_Valid(t *testing.T) {
	c := mustNewClient(t, &mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	})
	err := c.ValidateRepo("/some/repo")
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

func TestNewClient_AllowAllPaths(t *testing.T) {
	c, err := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	}, nil, true)
	require.NoError(t, err)
	err = c.ValidateRepo("/any/repo")
	require.NoError(t, err)
}
