package exec

import (
	"context"
	"errors"
	"os"
	oexec "os/exec"
	"sync"
)

const maxOutputBytes = 1 << 20

var errOutputTruncated = errors.New("command output exceeded 1 MiB limit")

// Runner executes context-aware subprocesses without an implicit shell.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	RunEnv(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
	Interactive(ctx context.Context, dir, name string, args ...string) error
}

// OSRunner executes real subprocesses.
type OSRunner struct{}

type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := b.limit
	if limit == 0 {
		limit = maxOutputBytes
	}
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
		b.truncated = true
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
func (b *boundedOutput) Truncated() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.truncated }

func run(cmd *oexec.Cmd) ([]byte, error) {
	configureProcess(cmd)
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if output.Truncated() {
		err = errors.Join(err, errOutputTruncated)
	}
	return output.Bytes(), err
}

func (OSRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := oexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	return run(cmd)
}

func (OSRunner) RunEnv(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := oexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	cmd.Env = env
	return run(cmd)
}

func (OSRunner) Interactive(ctx context.Context, dir, name string, args ...string) error {
	cmd := oexec.CommandContext(ctx, name, args...) //nolint:gosec // trusted executable selected by the application
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureProcess(cmd)
	return cmd.Run()
}
