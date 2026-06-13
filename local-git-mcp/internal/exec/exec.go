package exec

import (
	"context"
	oexec "os/exec"
)

// Runner abstracts command execution for testability.
type Runner interface {
	// Run executes a command and returns its combined output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// RunDir executes a command in a specific directory.
	RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a Runner that uses real OS commands.
func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return oexec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := oexec.CommandContext(ctx, name, args...) //nolint:gosec
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
