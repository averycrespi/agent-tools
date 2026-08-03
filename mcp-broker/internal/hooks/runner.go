package hooks

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

const terminateGracePeriod = 250 * time.Millisecond

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, handler Handler, payload []byte) RunStatus {
	if ctx.Err() != nil {
		return statusForContext(ctx)
	}

	cmd := exec.Command(handler.Command, handler.Args...) //nolint:gosec // handlers come only from trusted startup configuration
	cmd.Env = handler.Env
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		return RunStatusFailed
	}

	waited := make(chan error, 1)
	go func() {
		waited <- cmd.Wait()
	}()

	select {
	case err := <-waited:
		if err != nil {
			return RunStatusFailed
		}
		return RunStatusSucceeded
	case <-ctx.Done():
		terminateProcess(cmd.Process)
		timer := time.NewTimer(terminateGracePeriod)
		defer timer.Stop()
		select {
		case <-waited:
		case <-timer.C:
			killProcess(cmd.Process)
			<-waited
		}
		return statusForContext(ctx)
	}
}

func statusForContext(ctx context.Context) RunStatus {
	if ctx.Err() == context.DeadlineExceeded {
		return RunStatusTimedOut
	}
	return RunStatusCanceled
}
