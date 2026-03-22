package gh

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
