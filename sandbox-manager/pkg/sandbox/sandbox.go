package sandbox

import (
	"io"
	"log/slog"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/config"
	sbexec "github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
	"github.com/averycrespi/agent-tools/sandbox-manager/internal/lima"
	internalsandbox "github.com/averycrespi/agent-tools/sandbox-manager/internal/sandbox"
)

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Kill() error
}

type Client struct {
	service *internalsandbox.Service
}

func New() (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	runner := sbexec.NewOSRunner()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	return &Client{service: internalsandbox.NewService(lima.NewClient(runner), cfg, logger)}, nil
}

func (c *Client) Create() error {
	return c.service.Create()
}

func (c *Client) Exec(workdir string, args ...string) ([]byte, error) {
	return c.service.Exec(workdir, args...)
}

func (c *Client) StartPiped(workdir string, args ...string) (Process, error) {
	return c.service.ExecPiped(workdir, args...)
}
