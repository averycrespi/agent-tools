package exec

import (
	"context"
	osexec "os/exec"
)

// Runner abstracts command execution for testability. The context is
// honored for cancellation and deadlines — callers typically pass the
// request context so client disconnects and server shutdown terminate
// in-flight subprocesses.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSRunner implements Runner using os/exec.
type OSRunner struct{}

// NewOSRunner returns a Runner that uses real OS commands.
func NewOSRunner() *OSRunner { return &OSRunner{} }

func (r *OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return osexec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec
}
