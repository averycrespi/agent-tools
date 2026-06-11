package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
)

// Remote represents a configured git remote.
type Remote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetch_url"`
	PushURL  string `json:"push_url"`
}

// Ref represents a ref on a remote.
type Ref struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

// Client wraps git remote operations with an injectable command runner.
type Client struct {
	runner        exec.Runner
	allowedPaths  []string
	allowAllPaths bool
}

// NewClient returns a git Client using the given command runner.
func NewClient(runner exec.Runner, allowedPaths []string, allowAllPaths bool) (*Client, error) {
	normalizedPaths, err := normalizeAllowedPaths(allowedPaths, allowAllPaths)
	if err != nil {
		return nil, err
	}
	return &Client{runner: runner, allowedPaths: normalizedPaths, allowAllPaths: allowAllPaths}, nil
}

// Push pushes commits to a remote.
// If force is true, uses --force-with-lease.
func (c *Client) Push(ctx context.Context, repoPath, remote, refspec string, force bool) (string, error) {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, "--", remote)
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git push failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Pull pulls from a remote.
// If rebase is true, uses --rebase.
func (c *Client) Pull(ctx context.Context, repoPath, remote, branch string, rebase bool) (string, error) {
	args := []string{"pull"}
	if rebase {
		args = append(args, "--rebase")
	}
	args = append(args, "--", remote)
	if branch != "" {
		args = append(args, branch)
	}
	out, err := c.runner.RunDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git pull failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Fetch fetches from a remote without merging.
func (c *Client) Fetch(ctx context.Context, repoPath, remote, refspec string) (string, error) {
	args := []string{"fetch", "--", remote}
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runner.RunDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git fetch failed: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListRemoteRefs lists refs on a remote (branches, tags, etc.).
func (c *Client) ListRemoteRefs(ctx context.Context, repoPath, remote string) ([]Ref, error) {
	out, err := c.runner.RunDir(ctx, repoPath, "git", "ls-remote", "--", remote)
	if err != nil {
		return nil, fmt.Errorf("git ls-remote failed: %s", strings.TrimSpace(string(out)))
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		refs = append(refs, Ref{SHA: parts[0], Ref: parts[1]})
	}
	return refs, nil
}

// ListRemotes lists configured remotes with their URLs.
func (c *Client) ListRemotes(ctx context.Context, repoPath string) ([]Remote, error) {
	out, err := c.runner.RunDir(ctx, repoPath, "git", "remote", "-v")
	if err != nil {
		return nil, fmt.Errorf("git remote failed: %s", strings.TrimSpace(string(out)))
	}
	seen := make(map[string]*Remote)
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "<name>\t<url> (fetch)" or "<name>\t<url> (push)"
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		rest := parts[1]
		var url, kind string
		switch {
		case strings.HasSuffix(rest, " (fetch)"):
			url = strings.TrimSuffix(rest, " (fetch)")
			kind = "fetch"
		case strings.HasSuffix(rest, " (push)"):
			url = strings.TrimSuffix(rest, " (push)")
			kind = "push"
		default:
			continue
		}
		if _, ok := seen[name]; !ok {
			seen[name] = &Remote{Name: name}
			order = append(order, name)
		}
		if kind == "fetch" {
			seen[name].FetchURL = url
		} else {
			seen[name].PushURL = url
		}
	}
	remotes := make([]Remote, 0, len(order))
	for _, name := range order {
		remotes = append(remotes, *seen[name])
	}
	return remotes, nil
}

// ValidateRepo checks that the given path is allowed, absolute, and is a git repository.
func (c *Client) ValidateRepo(ctx context.Context, repoPath string) (string, error) {
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("repo_path must be an absolute path: %s", repoPath)
	}
	cleanRepoPath := filepath.Clean(repoPath)
	if !c.isAllowedPath(cleanRepoPath) {
		return "", fmt.Errorf("repo_path %s is outside allowed paths; allowed prefixes: %s", cleanRepoPath, strings.Join(c.allowedPaths, ", "))
	}
	out, err := c.runner.RunDir(ctx, cleanRepoPath, "git", "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", strings.TrimSpace(string(out)))
	}
	return cleanRepoPath, nil
}

func normalizeAllowedPaths(allowedPaths []string, allowAllPaths bool) ([]string, error) {
	if allowAllPaths {
		if len(allowedPaths) > 0 {
			return nil, fmt.Errorf("--allow-all-paths cannot be combined with explicit allowed paths")
		}
		return []string{"<all paths>"}, nil
	}
	if len(allowedPaths) == 0 {
		return nil, fmt.Errorf("at least one allowed path is required, or pass --allow-all-paths to allow all paths")
	}
	seen := make(map[string]bool)
	normalizedPaths := make([]string, 0, len(allowedPaths))
	for _, allowedPath := range allowedPaths {
		if !filepath.IsAbs(allowedPath) {
			return nil, fmt.Errorf("allowed path must be absolute: %s", allowedPath)
		}
		cleanPath := filepath.Clean(allowedPath)
		if !seen[cleanPath] {
			seen[cleanPath] = true
			normalizedPaths = append(normalizedPaths, cleanPath)
		}
	}
	return normalizedPaths, nil
}

func (c *Client) isAllowedPath(repoPath string) bool {
	if c.allowAllPaths {
		return true
	}
	for _, allowedPath := range c.allowedPaths {
		if repoPath == allowedPath {
			return true
		}
		rel, err := filepath.Rel(allowedPath, repoPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
