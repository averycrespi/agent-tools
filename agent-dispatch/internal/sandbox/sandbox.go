package sandbox

import (
	"fmt"
	"strings"

	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
)

type Client struct {
	runner adexec.Runner
}

func NewClient(runner adexec.Runner) *Client { return &Client{runner: runner} }

func (c *Client) Create() error {
	out, err := c.runner.Run("sb", "create")
	if err != nil {
		return fmt.Errorf("sb create failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (c *Client) Exec(workdir string, args ...string) ([]byte, error) {
	cmdArgs := execArgs(workdir, args...)
	out, err := c.runner.Run("sb", cmdArgs...)
	if err != nil {
		return out, fmt.Errorf("sb exec failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func (c *Client) StartPiped(workdir string, args ...string) (adexec.Process, error) {
	proc, err := c.runner.StartPiped("sb", execArgs(workdir, args...)...)
	if err != nil {
		return nil, fmt.Errorf("sb exec failed: %w", err)
	}
	return proc, nil
}

func execArgs(workdir string, args ...string) []string {
	cmdArgs := []string{"exec", "--workdir", workdir, "--"}
	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

func (c *Client) CheckWorktreeVisible(worktreePath string) error {
	if _, err := c.Exec("/", "test", "-d", worktreePath); err != nil {
		return fmt.Errorf("worktree is not visible inside the sandbox: %s\n\nAdd the worktree base directory as a writable sb mount, then recreate the Lima VM so mount changes apply", worktreePath)
	}
	return nil
}
