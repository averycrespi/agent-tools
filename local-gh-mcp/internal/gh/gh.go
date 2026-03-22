package gh

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/averycrespi/agent-tools/local-gh-mcp/internal/exec"
)

var validNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateOwnerRepo checks that owner and repo contain only safe characters.
func ValidateOwnerRepo(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner is required")
	}
	if repo == "" {
		return fmt.Errorf("repo is required")
	}
	if !validNamePattern.MatchString(owner) {
		return fmt.Errorf("invalid owner: %q", owner)
	}
	if !validNamePattern.MatchString(repo) {
		return fmt.Errorf("invalid repo: %q", repo)
	}
	return nil
}

// Client wraps gh CLI operations with an injectable command runner.
type Client struct {
	runner exec.Runner
}

// NewClient returns a Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

// repoFlag returns the -R flag value for targeting a specific repo.
func repoFlag(owner, repo string) string {
	return owner + "/" + repo
}

// AuthStatus checks whether the gh CLI is authenticated.
func (c *Client) AuthStatus(_ context.Context) error {
	out, err := c.runner.Run("gh", "auth", "status")
	if err != nil {
		return fmt.Errorf("gh auth status failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
