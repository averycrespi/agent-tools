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

// Push pushes commits to a remote.
// If force is true, uses --force-with-lease.
func (c *Client) Push(repoPath, remote, refspec string, force bool) (string, error) {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git push failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Pull pulls from a remote.
// If rebase is true, uses --rebase.
func (c *Client) Pull(repoPath, remote, branch string, rebase bool) (string, error) {
	args := []string{"pull"}
	if rebase {
		args = append(args, "--rebase")
	}
	args = append(args, remote)
	if branch != "" {
		args = append(args, branch)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git pull failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Fetch fetches from a remote without merging.
func (c *Client) Fetch(repoPath, remote, refspec string) (string, error) {
	args := []string{"fetch", remote}
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git fetch failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListRemoteRefs lists refs on a remote (branches, tags, etc.).
func (c *Client) ListRemoteRefs(repoPath, remote string) (string, error) {
	out, err := c.runner.RunDir(repoPath, "git", "ls-remote", remote)
	if err != nil {
		return "", fmt.Errorf("git ls-remote failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListRemotes lists configured remotes with their URLs.
func (c *Client) ListRemotes(repoPath string) (string, error) {
	out, err := c.runner.RunDir(repoPath, "git", "remote", "-v")
	if err != nil {
		return "", fmt.Errorf("git remote failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
