package exec

import (
	"context"
	"os"
	osexec "os/exec"
)

// Runner executes context-aware subprocesses without an implicit shell.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	RunEnv(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
	Interactive(ctx context.Context, dir, name string, args ...string) error
}

// OSRunner executes real subprocesses.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := osexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (OSRunner) RunEnv(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := osexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func (OSRunner) Interactive(ctx context.Context, dir, name string, args ...string) error {
	cmd := osexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
