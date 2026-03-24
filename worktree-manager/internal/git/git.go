package git

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
)

// Info contains information about a git repository.
type Info struct {
	Name       string // Repository directory name
	Root       string // Absolute path to repository root
	IsWorktree bool   // True if path is inside a worktree (not the main repo)
}

// Client wraps git operations with an injectable command runner.
type Client struct {
	runner exec.Runner
}

// NewClient returns a git Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

// RepoInfo returns information about the git repository at the given path.
func (c *Client) RepoInfo(path string) (Info, error) {
	if out, err := c.runner.RunDir(path, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return Info{}, fmt.Errorf("not a git repository: %s", strings.TrimSpace(string(out)))
	}

	out, err := c.runner.RunDir(path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, fmt.Errorf("could not determine repo root: %w", err)
	}
	root := strings.TrimSpace(string(out))

	out, err = c.runner.RunDir(path, "git", "rev-parse", "--git-dir")
	if err != nil {
		return Info{}, fmt.Errorf("could not determine git dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))

	out, err = c.runner.RunDir(path, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return Info{}, fmt.Errorf("could not determine git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))

	// In the main repo, --git-dir and --git-common-dir resolve to the same
	// path. In a worktree, --git-dir points to .git/worktrees/<name> while
	// --git-common-dir points to the main repo's .git directory.
	absGitDir := absOrJoin(path, gitDir)
	absCommonDir := absOrJoin(path, commonDir)
	isWorktree := absGitDir != absCommonDir

	return Info{
		Name:       filepath.Base(root),
		Root:       root,
		IsWorktree: isWorktree,
	}, nil
}

// absOrJoin returns p as-is if it is already absolute, otherwise joins it
// with base to produce an absolute path. This is used to normalize the
// relative paths returned by git rev-parse.
func absOrJoin(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// BranchExists checks if a local branch exists.
func (c *Client) BranchExists(repoRoot, branch string) bool {
	_, err := c.runner.RunDir(repoRoot, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// ListBranches returns the names of all local branches.
func (c *Client) ListBranches(repoRoot string) ([]string, error) {
	out, err := c.runner.RunDir(repoRoot, "git", "branch", "--list", "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("git branch list failed: %s", strings.TrimSpace(string(out)))
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// AddWorktree creates a git worktree at the given path.
// If the branch exists locally, it checks it out. Otherwise, it creates a new branch.
func (c *Client) AddWorktree(repoRoot, path, branch string) error {
	if c.BranchExists(repoRoot, branch) {
		out, err := c.runner.RunDir(repoRoot, "git", "worktree", "add", "--quiet", path, branch)
		if err != nil {
			return fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	} else {
		out, err := c.runner.RunDir(repoRoot, "git", "worktree", "add", "--quiet", "-b", branch, path)
		if err != nil {
			return fmt.Errorf("git worktree add -b failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// RemoveWorktree removes a git worktree at the given path.
func (c *Client) RemoveWorktree(repoRoot, path string) error {
	out, err := c.runner.RunDir(repoRoot, "git", "worktree", "remove", path)
	if err != nil {
		return fmt.Errorf("git worktree remove failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteBranch deletes a local git branch.
// If force is true, uses -D (force delete). Otherwise uses -d (safe delete).
func (c *Client) DeleteBranch(repoRoot, branch string, force bool) error {
	// Check if the branch exists before attempting to delete it.
	if _, err := c.runner.RunDir(repoRoot, "git", "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return nil
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	out, err := c.runner.RunDir(repoRoot, "git", "branch", flag, branch)
	if err != nil {
		return fmt.Errorf("git branch delete failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CommonDir returns the git common directory for the repo at path.
// For worktrees this points back to the main repo's .git directory.
func (c *Client) CommonDir(path string) (string, error) {
	out, err := c.runner.RunDir(path, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("could not determine git common dir: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
