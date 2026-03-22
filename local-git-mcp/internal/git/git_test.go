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
