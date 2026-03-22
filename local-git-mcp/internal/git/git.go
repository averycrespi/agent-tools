package git

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
)

// Client wraps git remote operations with an injectable command runner.
type Client struct {
	runner exec.Runner
}

// NewClient returns a git Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

// ValidateRepo checks that the given path is absolute and is a git repository.
func (c *Client) ValidateRepo(repoPath string) error {
	if !filepath.IsAbs(repoPath) {
		return fmt.Errorf("repo_path must be an absolute path: %s", repoPath)
	}
	out, err := c.runner.RunDir(repoPath, "git", "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("not a git repository: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
