package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/local-git-mcp/internal/exec"
)

const DefaultCommandTimeout = 5 * time.Minute

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

// CloneGitHubResult represents the path created by a GitHub clone operation.
type CloneGitHubResult struct {
	RepoPath string `json:"repo_path"`
}

// Client wraps git remote operations with an injectable command runner.
type Client struct {
	runner         exec.Runner
	allowedPaths   []string
	allowAllPaths  bool
	commandTimeout time.Duration
}

// NewClient returns a git Client using the given command runner.
func NewClient(runner exec.Runner, allowedPaths []string, allowAllPaths bool) (*Client, error) {
	return NewClientWithTimeout(runner, allowedPaths, allowAllPaths, DefaultCommandTimeout)
}

// NewClientWithTimeout returns a git Client using the given command runner and per-command timeout.
func NewClientWithTimeout(runner exec.Runner, allowedPaths []string, allowAllPaths bool, commandTimeout time.Duration) (*Client, error) {
	normalizedPaths, err := normalizeAllowedPaths(allowedPaths, allowAllPaths)
	if err != nil {
		return nil, err
	}
	return &Client{runner: runner, allowedPaths: normalizedPaths, allowAllPaths: allowAllPaths, commandTimeout: commandTimeout}, nil
}

// CloneGitHubRepo clones a GitHub repository over SSH into destinationDir.
func (c *Client) CloneGitHubRepo(ctx context.Context, repository, destinationDir string) (string, error) {
	owner, repo, err := parseGitHubRepository(repository)
	if err != nil {
		return "", err
	}
	cleanDestinationDir, targetPath, err := c.validateCloneTarget(destinationDir, repo)
	if err != nil {
		return "", err
	}

	sshURL := fmt.Sprintf("git@github.com:%s/%s.git", owner, repo)
	out, err := c.runDir(ctx, cleanDestinationDir, "git", "clone", "--", sshURL, targetPath)
	if err != nil {
		return "", fmt.Errorf("git clone failed: %s", commandErrorMessage(out, err))
	}
	if _, err := c.ValidateRepo(ctx, targetPath); err != nil {
		return "", fmt.Errorf("validating cloned repository: %w", err)
	}
	return targetPath, nil
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
	out, err := c.runDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git push failed: %s", commandErrorMessage(out, err))
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
	out, err := c.runDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git pull failed: %s", commandErrorMessage(out, err))
	}
	return strings.TrimSpace(string(out)), nil
}

// Fetch fetches from a remote without merging.
func (c *Client) Fetch(ctx context.Context, repoPath, remote, refspec string) (string, error) {
	args := []string{"fetch", "--", remote}
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := c.runDir(ctx, repoPath, "git", args...)
	if err != nil {
		return "", fmt.Errorf("git fetch failed: %s", commandErrorMessage(out, err))
	}
	return strings.TrimSpace(string(out)), nil
}

// ListRemoteRefs lists refs on a remote (branches, tags, etc.).
func (c *Client) ListRemoteRefs(ctx context.Context, repoPath, remote string) ([]Ref, error) {
	out, err := c.runDir(ctx, repoPath, "git", "ls-remote", "--", remote)
	if err != nil {
		return nil, fmt.Errorf("git ls-remote failed: %s", commandErrorMessage(out, err))
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
	out, err := c.runDir(ctx, repoPath, "git", "remote", "-v")
	if err != nil {
		return nil, fmt.Errorf("git remote failed: %s", commandErrorMessage(out, err))
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
	out, err := c.runDir(ctx, cleanRepoPath, "git", "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", commandErrorMessage(out, err))
	}
	return cleanRepoPath, nil
}

func parseGitHubRepository(repository string) (string, string, error) {
	if repository == "" {
		return "", "", fmt.Errorf("repository is required")
	}
	if strings.TrimSpace(repository) != repository {
		return "", "", fmt.Errorf("repository must be in owner/repo form: %s", repository)
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validGitHubOwner(parts[0]) || !validGitHubRepo(parts[1]) {
		return "", "", fmt.Errorf("repository must be in owner/repo form: %s", repository)
	}
	return parts[0], parts[1], nil
}

func validGitHubOwner(owner string) bool {
	if owner == "" || owner == "." || owner == ".." || strings.HasPrefix(owner, "-") || strings.HasSuffix(owner, "-") {
		return false
	}
	for _, r := range owner {
		if !isASCIILetterOrDigit(r) && r != '-' {
			return false
		}
	}
	return true
}

func validGitHubRepo(repo string) bool {
	if repo == "" || repo == "." || repo == ".." {
		return false
	}
	for _, r := range repo {
		if !isASCIILetterOrDigit(r) && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func isASCIILetterOrDigit(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func (c *Client) validateCloneTarget(destinationDir, repo string) (string, string, error) {
	if !filepath.IsAbs(destinationDir) {
		return "", "", fmt.Errorf("destination_dir must be an absolute path: %s", destinationDir)
	}
	cleanDestinationDir := filepath.Clean(destinationDir)
	if !c.isAllowedPath(cleanDestinationDir) {
		return "", "", fmt.Errorf("destination_dir %s is outside allowed paths; allowed prefixes: %s", cleanDestinationDir, strings.Join(c.allowedPaths, ", "))
	}
	info, err := os.Lstat(cleanDestinationDir)
	if err != nil {
		return "", "", fmt.Errorf("destination_dir must be an existing directory: %s", cleanDestinationDir)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("destination_dir must not be a symlink: %s", cleanDestinationDir)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("destination_dir must be a directory: %s", cleanDestinationDir)
	}
	targetPath := filepath.Join(cleanDestinationDir, repo)
	if _, err := os.Lstat(targetPath); err == nil {
		return "", "", fmt.Errorf("target directory already exists: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("checking target directory: %w", err)
	}
	return cleanDestinationDir, targetPath, nil
}

func (c *Client) runDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.commandTimeout <= 0 {
		return c.runner.RunDir(ctx, dir, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()
	return c.runner.RunDir(ctx, dir, name, args...)
}

func commandErrorMessage(out []byte, err error) string {
	message := strings.TrimSpace(string(out))
	if message != "" {
		return message
	}
	return err.Error()
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
