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

func TestPush_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Everything up-to-date\n"), nil
		},
	})
	out, err := c.Push("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Everything up-to-date", out)
	assert.Equal(t, []string{"push", "origin"}, capturedArgs)
}

func TestPush_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "refs/heads/main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "origin", "refs/heads/main"}, capturedArgs)
}

func TestPush_ForceWithLease(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Push("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"push", "--force-with-lease", "origin"}, capturedArgs)
}

func TestPush_Error(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("error: failed to push"), fmt.Errorf("exit status 1")
		},
	})
	_, err := c.Push("/repo", "origin", "", false)
	assert.ErrorContains(t, err, "git push failed")
}

func TestPull_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("Already up to date.\n"), nil
		},
	})
	out, err := c.Pull("/repo", "origin", "", false)
	require.NoError(t, err)
	assert.Equal(t, "Already up to date.", out)
	assert.Equal(t, []string{"pull", "origin"}, capturedArgs)
}

func TestPull_WithBranch(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "main", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "origin", "main"}, capturedArgs)
}

func TestPull_WithRebase(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Pull("/repo", "origin", "", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"pull", "--rebase", "origin"}, capturedArgs)
}

func TestFetch_DefaultArgs(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "origin"}, capturedArgs)
}

func TestFetch_WithRefspec(t *testing.T) {
	var capturedArgs []string
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			capturedArgs = args
			return nil, nil
		},
	})
	_, err := c.Fetch("/repo", "origin", "refs/heads/main")
	require.NoError(t, err)
	assert.Equal(t, []string{"fetch", "origin", "refs/heads/main"}, capturedArgs)
}

func TestValidateRepo_RelativePath(t *testing.T) {
	c := NewClient(&mockRunner{})
	err := c.ValidateRepo("relative/path")
	assert.ErrorContains(t, err, "must be an absolute path")
}

func TestValidateRepo_NotAGitRepo(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte("fatal: not a git repository"), fmt.Errorf("exit status 128")
		},
	})
	err := c.ValidateRepo("/some/path")
	assert.ErrorContains(t, err, "not a git repository")
}

func TestValidateRepo_Valid(t *testing.T) {
	c := NewClient(&mockRunner{
		runDirFunc: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(".git\n"), nil
		},
	})
	err := c.ValidateRepo("/some/repo")
	require.NoError(t, err)
}
