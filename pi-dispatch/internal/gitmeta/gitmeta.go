package gitmeta

import (
	"fmt"
	"path/filepath"
	"strings"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
)

type Info struct {
	Root          string
	Name          string
	CurrentBranch string
}

type Client struct {
	runner pdexec.Runner
}

func NewClient(runner pdexec.Runner) *Client {
	return &Client{runner: runner}
}

func (c *Client) Info(path string) (Info, error) {
	rootOut, err := c.runner.RunDir(path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return Info{}, fmt.Errorf("failed to determine repo root: %w", err)
	}
	branchOut, err := c.runner.RunDir(path, "git", "branch", "--show-current")
	if err != nil {
		return Info{}, fmt.Errorf("failed to determine current branch: %w", err)
	}
	root := strings.TrimSpace(string(rootOut))
	return Info{Root: root, Name: filepath.Base(root), CurrentBranch: strings.TrimSpace(string(branchOut))}, nil
}
