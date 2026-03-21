package exec

import (
	"os"
	osexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(name string, args ...string) ([]byte, error)

	// RunInteractive executes a command with stdin/stdout/stderr connected.
	RunInteractive(name string, args ...string) error
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a new OSRunner.
func NewOSRunner() *OSRunner { return &OSRunner{} }

// Run executes a command and returns combined stdout+stderr.
func (r *OSRunner) Run(name string, args ...string) ([]byte, error) {
	return osexec.Command(name, args...).CombinedOutput() //nolint:gosec
}

// RunInteractive executes a command with stdin/stdout/stderr attached.
func (r *OSRunner) RunInteractive(name string, args ...string) error {
	cmd := osexec.Command(name, args...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
