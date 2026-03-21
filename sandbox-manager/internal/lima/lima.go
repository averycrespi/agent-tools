package lima

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/sandbox-manager/internal/exec"
)

const vmName = "sb"

// Status represents the state of the Lima VM.
type Status string

const (
	StatusRunning    Status = "Running"
	StatusStopped    Status = "Stopped"
	StatusNotCreated Status = "NotCreated"
)

// Client wraps limactl commands.
type Client struct {
	runner exec.Runner
}

// NewClient returns a new Lima client.
func NewClient(runner exec.Runner) *Client {
	return &Client{runner: runner}
}

type instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Status returns the current VM status.
func (c *Client) Status() (Status, error) {
	out, err := c.runner.Run("limactl", "list", "--json")
	if err != nil {
		return "", fmt.Errorf("failed to list VMs: %w", err)
	}

	// limactl list --json outputs one JSON object per line.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var inst instance
		if err := json.Unmarshal([]byte(line), &inst); err != nil {
			continue
		}
		if inst.Name == vmName {
			switch inst.Status {
			case "Running":
				return StatusRunning, nil
			case "Stopped":
				return StatusStopped, nil
			default:
				return StatusStopped, nil
			}
		}
	}

	return StatusNotCreated, nil
}

// Create creates and starts a new VM from the given template.
func (c *Client) Create(templatePath string) error {
	if _, err := c.runner.Run("limactl", "start", "--name="+vmName, templatePath); err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}
	return nil
}

// Start starts a stopped VM.
func (c *Client) Start() error {
	if _, err := c.runner.Run("limactl", "start", vmName); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}
	return nil
}

// Stop stops a running VM.
func (c *Client) Stop() error {
	if _, err := c.runner.Run("limactl", "stop", vmName); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}
	return nil
}

// Delete removes the VM. Uses --force to skip interactive confirmation.
func (c *Client) Delete() error {
	if _, err := c.runner.Run("limactl", "delete", "--force", vmName); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}
	return nil
}

// Copy copies a file or directory from the host to the VM.
// If recursive is true, the -r flag is passed to limactl cp.
func (c *Client) Copy(localPath, guestPath string, recursive bool) error {
	dst := vmName + ":" + guestPath
	args := []string{"cp"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, localPath, dst)
	if _, err := c.runner.Run("limactl", args...); err != nil {
		return fmt.Errorf("failed to copy %q to %q: %w", localPath, guestPath, err)
	}
	return nil
}

// Exec runs a command in the VM and returns its output.
func (c *Client) Exec(args ...string) ([]byte, error) {
	cmdArgs := []string{"shell", "--workdir", "/", vmName, "--"}
	cmdArgs = append(cmdArgs, args...)
	out, err := c.runner.Run("limactl", cmdArgs...)
	if err != nil {
		return out, fmt.Errorf("failed to exec in VM: %w", err)
	}
	return out, nil
}

// Shell opens an interactive shell in the VM, or runs a command if args are provided.
func (c *Client) Shell(args ...string) error {
	cmdArgs := []string{"shell", vmName}
	if len(args) > 0 {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, args...)
	}
	if err := c.runner.RunInteractive("limactl", cmdArgs...); err != nil {
		return fmt.Errorf("failed to open shell: %w", err)
	}
	return nil
}
