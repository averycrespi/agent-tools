package exec

import (
	"context"
	"fmt"
	osexec "os/exec"
	"time"
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
	return osexec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec
}

func (r *OSRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := osexec.CommandContext(ctx, name, args...) //nolint:gosec
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

type TimeoutRunner struct {
	inner   Runner
	timeout time.Duration
}

func NewTimeoutRunner(inner Runner, timeout time.Duration) *TimeoutRunner {
	return &TimeoutRunner{inner: inner, timeout: timeout}
}

func (r *TimeoutRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	out, err := r.inner.Run(ctx, name, args...)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("command timed out after %s: %w", r.timeout, ctx.Err())
	}
	return out, err
}

func (r *TimeoutRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	out, err := r.inner.RunDir(ctx, dir, name, args...)
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("command timed out after %s: %w", r.timeout, ctx.Err())
	}
	return out, err
}
