package tmux

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
)

const SocketName = "wt"

// Client wraps tmux operations with an injectable command runner.
type Client struct {
	runner  exec.Runner
	TmuxEnv string // value of $TMUX, used to detect if already inside wt socket
}

// NewClient returns a tmux Client using the given command runner.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

func (c *Client) tmuxArgs(args ...string) []string {
	return append([]string{"-L", SocketName}, args...)
}

func (c *Client) run(args ...string) ([]byte, error) {
	return c.runner.Run("tmux", c.tmuxArgs(args...)...)
}

// SessionExists returns true if the named tmux session exists.
func (c *Client) SessionExists(name string) bool {
	_, err := c.run("has-session", "-t", name)
	return err == nil
}

// CreateSession creates a new detached tmux session with the given window name.
func (c *Client) CreateSession(name, windowName string) error {
	out, err := c.run("new-session", "-d", "-s", name, "-n", windowName)
	if err != nil {
		return fmt.Errorf("tmux new-session failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// WindowExists returns true if the named window exists in the session.
func (c *Client) WindowExists(session, window string) bool {
	windows, err := c.ListWindows(session)
	if err != nil {
		return false
	}
	for _, w := range windows {
		if w == window {
			return true
		}
	}
	return false
}

// CreateWindow creates a new window in the session with the given working directory.
func (c *Client) CreateWindow(session, window, cwd string) error {
	out, err := c.run("new-window", "-t", session, "-n", window, "-c", cwd, "-d")
	if err != nil {
		return fmt.Errorf("tmux new-window failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// KillWindow destroys a window in the session.
func (c *Client) KillWindow(session, window string) error {
	out, err := c.run("kill-window", "-t", session+":"+window)
	if err != nil {
		return fmt.Errorf("tmux kill-window failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// KillSession destroys the named session.
func (c *Client) KillSession(name string) error {
	out, err := c.run("kill-session", "-t", name)
	if err != nil {
		return fmt.Errorf("tmux kill-session failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SendKeys sends a command to the target window followed by Enter.
func (c *Client) SendKeys(session, window, command string) error {
	out, err := c.run("send-keys", "-t", session+":"+window, command, "C-m")
	if err != nil {
		return fmt.Errorf("tmux send-keys failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ListWindows returns the names of all windows in the session.
func (c *Client) ListWindows(session string) ([]string, error) {
	out, err := c.run("list-windows", "-t", session, "-F", "#{window_name}")
	if err != nil {
		return nil, fmt.Errorf("tmux list-windows failed: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// insideWtSocket returns true if the TMUX env var indicates we are inside a wt socket.
func insideWtSocket(tmuxEnv string) bool {
	if tmuxEnv == "" {
		return false
	}
	socketPath := tmuxEnv
	if i := strings.Index(tmuxEnv, ","); i >= 0 {
		socketPath = tmuxEnv[:i]
	}
	base := filepath.Base(socketPath)
	return base == SocketName
}

// Attach attaches to the named session, or switches if already inside the wt socket.
func (c *Client) Attach(session string) error {
	if insideWtSocket(c.TmuxEnv) {
		return c.runner.RunInteractive("tmux", c.tmuxArgs("switch-client", "-t", session)...)
	}
	return c.runner.RunInteractive("tmux", c.tmuxArgs("attach-session", "-t", session)...)
}

// AttachToWindow attaches to a specific window, or switches if already inside the wt socket.
func (c *Client) AttachToWindow(session, window string) error {
	target := session + ":" + window
	if insideWtSocket(c.TmuxEnv) {
		return c.runner.RunInteractive("tmux", c.tmuxArgs("switch-client", "-t", target)...)
	}
	return c.runner.RunInteractive("tmux", c.tmuxArgs("attach-session", "-t", target)...)
}
