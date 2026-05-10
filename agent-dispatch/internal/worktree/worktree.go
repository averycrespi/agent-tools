package worktree

import (
	"fmt"
	"strings"

	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
)

type Client struct {
	runner adexec.Runner
}

func NewClient(runner adexec.Runner) *Client { return &Client{runner: runner} }

func (c *Client) AddHeadless(repoPath, branch string) error {
	out, err := c.runner.RunDir(repoPath, "wt", "add", "--no-window", branch)
	if err != nil {
		return fmt.Errorf("wt add --no-window failed in %s: %s: %w", repoPath, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (c *Client) Path(repoPath, branch string) (string, error) {
	out, err := c.runner.RunDir(repoPath, "wt", "path", branch)
	if err != nil {
		return "", fmt.Errorf("wt path failed in %s: %s: %w", repoPath, strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
