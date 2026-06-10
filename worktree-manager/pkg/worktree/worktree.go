package worktree

import (
	"io"
	"log/slog"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/config"
	wtexec "github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/git"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/tmux"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/workspace"
)

type Client struct {
	service *workspace.Service
}

func New() (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	runner := wtexec.NewOSRunner()
	tmuxClient := tmux.NewClient(runner)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	return &Client{service: workspace.NewService(git.NewClient(runner), tmuxClient, cfg, logger, runner)}, nil
}

func (c *Client) AddHeadless(repoRoot, branch string) (string, error) {
	return c.service.AddHeadless(repoRoot, branch)
}

func (c *Client) AddHeadlessWithOwnership(repoRoot, branch string) (string, bool, error) {
	return c.service.AddHeadlessWithOwnership(repoRoot, branch)
}

func (c *Client) Path(repoRoot, branch string) (string, error) {
	return c.service.Path(repoRoot, branch)
}

func (c *Client) Remove(repoRoot, branch string) error {
	return c.service.Remove(repoRoot, branch, false, false)
}
