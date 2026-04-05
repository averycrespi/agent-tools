package git

import (
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
	args = append(args, "--", remote)
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
	args = append(args, "--", remote)
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
	args := []string{"fetch", "--", remote}
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
func (c *Client) ListRemoteRefs(repoPath, remote string) ([]Ref, error) {
	out, err := c.runner.RunDir(repoPath, "git", "ls-remote", "--", remote)
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
func (c *Client) ListRemotes(repoPath string) ([]Remote, error) {
	out, err := c.runner.RunDir(repoPath, "git", "remote", "-v")
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
		if strings.HasSuffix(rest, " (fetch)") {
			url = strings.TrimSuffix(rest, " (fetch)")
			kind = "fetch"
		} else if strings.HasSuffix(rest, " (push)") {
			url = strings.TrimSuffix(rest, " (push)")
			kind = "push"
		} else {
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
