package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
)

type runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type Repository struct {
	PrimaryRoot  string
	CommonGitDir string
	Identity     string
}

type Snapshot struct {
	Complete  bool       `json:"complete"`
	Worktrees []Worktree `json:"worktrees"`
}

type Client struct {
	runner  runner
	timeout time.Duration
}

func New(runner runner, timeout time.Duration) *Client {
	return &Client{runner: runner, timeout: timeout}
}

func (c *Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	output, err := c.runner.Run(ctx, dir, "git", args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (c *Client) InspectPrimary(ctx context.Context, path string) (Repository, error) {
	rootOut, err := c.run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("resolving primary worktree: %w", err)
	}
	root, err := config.CanonicalExisting(strings.TrimSpace(string(rootOut)))
	if err != nil {
		return Repository{}, err
	}
	bareOut, err := c.run(ctx, root, "rev-parse", "--is-bare-repository")
	if err != nil {
		return Repository{}, err
	}
	if strings.TrimSpace(string(bareOut)) == "true" {
		return Repository{}, fmt.Errorf("bare repositories cannot be registered")
	}
	gitOut, err := c.run(ctx, root, "rev-parse", "--git-dir")
	if err != nil {
		return Repository{}, err
	}
	commonOut, err := c.run(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, err
	}
	canonicalGit, err := canonicalGitPath(root, strings.TrimSpace(string(gitOut)))
	if err != nil {
		return Repository{}, err
	}
	common, err := canonicalGitPath(root, strings.TrimSpace(string(commonOut)))
	if err != nil {
		return Repository{}, err
	}
	if canonicalGit != common {
		return Repository{}, fmt.Errorf("linked worktree registration is not allowed; register the primary worktree")
	}
	return Repository{PrimaryRoot: root, CommonGitDir: common, Identity: common}, nil
}

func canonicalGitPath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return config.CanonicalExisting(path)
}

func (c *Client) ListRaw(ctx context.Context, primaryRoot string) ([]byte, error) {
	return c.run(ctx, primaryRoot, "worktree", "list", "--porcelain", "-z")
}

func (c *Client) Snapshot(ctx context.Context, repo Repository) (Snapshot, error) {
	data, err := c.ListRaw(ctx, repo.PrimaryRoot)
	if err != nil {
		return Snapshot{Complete: false}, err
	}
	worktrees, err := ParsePorcelainZ(data)
	if err != nil {
		return Snapshot{Complete: false}, err
	}
	complete := true
	var errs []error
	for i := range worktrees {
		if worktrees[i].Prunable != "" {
			worktrees[i].Exclusion = "prunable"
			continue
		}
		canonical, canonicalErr := config.CanonicalExisting(worktrees[i].Path)
		if canonicalErr != nil {
			worktrees[i].Exclusion = "missing"
			complete = false
			errs = append(errs, canonicalErr)
			continue
		}
		worktrees[i].Path = canonical
		gitDirOut, runErr := c.run(ctx, canonical, "rev-parse", "--git-dir")
		if runErr != nil {
			worktrees[i].Exclusion = "git-identity-unavailable"
			complete = false
			errs = append(errs, runErr)
			continue
		}
		identity, identityErr := canonicalGitPath(canonical, strings.TrimSpace(string(gitDirOut)))
		if identityErr != nil {
			worktrees[i].Exclusion = "git-identity-unavailable"
			complete = false
			errs = append(errs, identityErr)
			continue
		}
		worktrees[i].Identity = identity
	}
	return Snapshot{Complete: complete, Worktrees: worktrees}, errors.Join(errs...)
}

func (c *Client) Add(ctx context.Context, root, path, branch, start string, existing bool) error {
	args := []string{"worktree", "add", "--quiet"}
	if !existing {
		args = append(args, "-b", branch)
	}
	args = append(args, path)
	if existing {
		args = append(args, branch)
	} else if start != "" {
		args = append(args, start)
	}
	_, err := c.run(ctx, root, args...)
	return err
}

func (c *Client) BranchExists(ctx context.Context, root, branch string) (bool, error) {
	_, err := c.run(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

func (c *Client) Remove(ctx context.Context, root, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := c.run(ctx, root, args...)
	return err
}

func (c *Client) DeleteBranch(ctx context.Context, root, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := c.run(ctx, root, "branch", flag, branch)
	return err
}

func (c *Client) Prune(ctx context.Context, root string) error {
	_, err := c.run(ctx, root, "worktree", "prune")
	return err
}
