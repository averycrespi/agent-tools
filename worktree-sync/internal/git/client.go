package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
		return nil, fmt.Errorf("git command failed: %w", err)
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
	return Repository{PrimaryRoot: root, CommonGitDir: common}, nil
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
		if canonical == repo.PrimaryRoot {
			worktrees[i].Identity = repo.CommonGitDir
			continue
		}
		identity, identityErr := linkedGitDir(canonical)
		if identityErr == nil {
			worktrees[i].Identity, identityErr = worktreeIdentity(identity)
		}
		if identityErr != nil {
			worktrees[i].Exclusion = "git-identity-unavailable"
			complete = false
			errs = append(errs, identityErr)
		}
	}
	return Snapshot{Complete: complete, Worktrees: worktrees}, errors.Join(errs...)
}

func linkedGitDir(worktree string) (string, error) {
	file, err := os.Open(filepath.Join(worktree, ".git")) //nolint:gosec // canonical worktree path from Git
	if err != nil {
		return "", fmt.Errorf("opening linked-worktree Git file: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", fmt.Errorf("reading linked-worktree Git file: %w", err)
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("linked-worktree Git file is too large")
	}
	value := strings.TrimSpace(string(data))
	path, ok := strings.CutPrefix(value, "gitdir: ")
	if !ok || path == "" {
		return "", fmt.Errorf("linked-worktree Git file is malformed")
	}
	return canonicalGitPath(worktree, path)
}

func worktreeIdentity(gitDir string) (string, error) {
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", fmt.Errorf("stating Git administrative directory: %w", err)
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() {
		return "", fmt.Errorf("git administrative identity is unavailable for %s", gitDir)
	}
	device, inode := value.FieldByName("Dev"), value.FieldByName("Ino")
	if !device.IsValid() || !inode.IsValid() {
		return "", fmt.Errorf("git administrative identity is unavailable for %s", gitDir)
	}
	return fmt.Sprintf("%s#%v:%v", gitDir, device.Interface(), inode.Interface()), nil
}

func (c *Client) Add(ctx context.Context, root, path, branch, from string, existing bool) error {
	if existing && from != "" {
		return fmt.Errorf("branch %q already exists; --from applies only when creating a new branch", branch)
	}
	args := []string{"worktree", "add", "--quiet"}
	if !existing {
		args = append(args, "-b", branch)
	}
	args = append(args, path)
	if existing {
		args = append(args, branch)
	} else if from != "" {
		args = append(args, from)
	}
	_, err := c.run(ctx, root, args...)
	return err
}

func (c *Client) BranchExists(ctx context.Context, root, branch string) (bool, error) {
	_, err := c.run(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return false, nil
		}
		return false, err
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
